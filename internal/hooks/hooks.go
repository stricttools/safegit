// Package hooks discovers and executes pre-pre-push hooks that run before any network I/O, solving the SSH timeout problem when checks are long-running.
package hooks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// stdout and stderr are where a hook's stdout and its stderr are forwarded.
// BOTH default to safegit's stderr: safegit's stdout is a structured channel
// (the JSON envelope in machine mode), and an operator-supplied script must not
// be able to write into it, whichever stream it wrote to. Tests can override
// them via SetOutput to tell the two streams apart.
var (
	stdout io.Writer = os.Stderr
	stderr io.Writer = os.Stderr
)

// beforeStdoutRead runs in the reader just before it starts reading a hook's
// stdout. It does nothing in production; a test replaces it to delay the
// reader and prove that output is never lost to a slow reader.
var beforeStdoutRead = func() {}

// SetOutput overrides the package-level stdout and stderr writers.
// Returns a restore function that resets them to their previous values.
func SetOutput(out, err io.Writer) func() {
	prevOut, prevErr := stdout, stderr
	stdout, stderr = out, err
	return func() { stdout, stderr = prevOut, prevErr }
}

// HookResult holds the outcome of running a single hook. It is never emitted
// as it stands, so it carries no JSON spelling: a machine payload records a run
// through the caller's own record type, whose member names are the one
// documented form.
type HookResult struct {
	Name string
	// ExitCode is the hook's own exit status, and nil when it has none: it
	// could not be started (StartError), or the timeout or an interruption
	// killed it.
	ExitCode *int
	// StartError is non-empty when the hook could not be started at all --
	// exec refused the file -- and says why, e.g. "exec: permission denied".
	// A hook that started and exited nonzero is never a start error.
	StartError string
	Duration   time.Duration
	TimedOut   bool
	// Leftovers are the processes the hook left running when it ended.
	Leftovers []LeftoverProcess
	// LeftoverUnknown is non-empty when something the hook started was still
	// holding its output after it ended and safegit could not name it; it says
	// why.
	LeftoverUnknown string
	// stopCap is the cap the run's stop ran under, which the message naming a
	// process safegit could not stop quotes.
	stopCap time.Duration
}

// LeftoverProcess is one process a hook left running when it ended, or the
// hook's own process when the stop could not end it.
type LeftoverProcess struct {
	PID     int
	Command string
	// Killed reports that safegit stopped it. False means it is still running:
	// it was alive when the stop reached its cap.
	Killed bool
	// State is the process state /proc reported for a process still running at
	// the cap ("D" for uninterruptible sleep), and empty where the platform has
	// no /proc or the process was killed.
	State string
	// FoundState is the process state /proc reported when safegit found the
	// process ("S", "D", "Z", ...), and empty where the platform has no /proc.
	FoundState string
	// Hook marks the hook's own process, which the stop could not end.
	Hook bool
}

// Failed reports whether the run failed: a nonzero status, no status at all
// (the hook could not be started, or was killed), a timeout, or any process
// left behind.
func (r HookResult) Failed() bool {
	return r.ExitCode == nil || *r.ExitCode != 0 || r.TimedOut || len(r.Leftovers) > 0 || r.LeftoverUnknown != ""
}

// LeftoverMessages returns the error lines naming what the hook left behind:
// one per process, and one more when something could not be named.
func (r HookResult) LeftoverMessages() []string {
	return leftoverMessages(r, containmentPartial)
}

// leftoverMessages words the lines for a platform whose containment is
// partial or complete. Where it is partial, a killed process carries the same
// note as one still running: safegit stopped what it found, and cannot say the
// same of a process that got out of its reach.
func leftoverMessages(r HookResult, partial bool) []string {
	var msgs []string
	for _, l := range r.Leftovers {
		if !l.Killed {
			state := ""
			if l.State != "" {
				state = " (process state " + l.State + ")"
			}
			unstopped := fmt.Sprintf("safegit could not stop it within the %s cap on stopping a hook%s and exits without it", FormatStopCap(r.stopCap), state)
			if partial {
				unstopped += "; " + uncontainedNote
			}
			if l.Hook {
				msgs = append(msgs, fmt.Sprintf("hook %s (process %d) is still running: %s", r.Name, l.PID, unstopped))
			} else {
				msgs = append(msgs, fmt.Sprintf("hook %s left process %d (%s) running after it ended; it is still running: %s", r.Name, l.PID, l.Command, unstopped))
			}
			continue
		}
		fate := "it was killed"
		if partial {
			fate += "; " + uncontainedNote
		}
		msgs = append(msgs, fmt.Sprintf("hook %s left process %d (%s) running after it ended; %s", r.Name, l.PID, l.Command, fate))
	}
	if r.LeftoverUnknown != "" {
		msgs = append(msgs, fmt.Sprintf("hook %s left a process holding its output after it ended, and safegit could not name it: %s; it may still be running; %s",
			r.Name, r.LeftoverUnknown, uncontainedNote))
	}
	return msgs
}

// LegacyLocationError reports hooks still sitting in the pre-migration
// location, git's own .git/hooks. Discovery refuses rather than running them:
// running from both places would make the store safegit executes from depend on
// where a file happened to be left, and silently skipping them would stop an
// operator's checks without saying so.
type LegacyLocationError struct {
	// Paths are the absolute paths found, in enumeration order.
	Paths []string
}

func (e *LegacyLocationError) Error() string {
	return fmt.Sprintf("%d hook(s) are still in the pre-migration location (%s); "+
		"run `safegit hook migrate` to move them into the tool-owned hook store", len(e.Paths), strings.Join(e.Paths, ", "))
}

// TrackedNotExecutableError reports a repository-provided hook whose mode says
// it cannot run. It is a refusal, not a skip: a hook in the checkout's
// .safegit/hooks is disabled by REMOVING it (and committing that), so a mode
// that silently disabled one would turn an accidentally lost executable bit --
// a checkout on a filesystem without modes, a patch applied by a tool that
// drops them -- into checks that quietly stopped running.
type TrackedNotExecutableError struct {
	// Paths are the absolute paths of the offending repository-provided hooks.
	Paths []string
}

func (e *TrackedNotExecutableError) Error() string {
	return fmt.Sprintf("%d repository-provided hook(s) in the checkout's .safegit/hooks are not executable: %s; "+
		"run `chmod +x` on each and COMMIT the mode change (such a hook is disabled by deleting the file and committing that, never by dropping its mode)",
		len(e.Paths), strings.Join(e.Paths, ", "))
}

// LocalNotExecutableError reports a hook in the tool-owned live store whose
// mode says it cannot run. It is the live store's half of the same rule the
// checkout-provided store has always been held to: a hook is disabled by
// REMOVING it, so a mode that silently disabled one would turn an accident --
// an editor that rewrote the file, a patch tool that dropped the bit, a copy
// across a filesystem without modes -- into checks that quietly stopped
// running. It used to be a skip with a warning on stderr, which is git's own
// stance for its own hooks; the two stores answering the same accident
// differently is what that stance cost, so it is a refusal now.
type LocalNotExecutableError struct {
	// Paths are the absolute paths of the offending hooks.
	Paths []string
}

func (e *LocalNotExecutableError) Error() string {
	return fmt.Sprintf("%d hook(s) in the tool-owned hook store are not executable: %s; "+
		"run `chmod +x` on each, or remove the hook (`safegit hook remove <name>`) if it is meant to be gone -- a hook is disabled by removing it, never by dropping its mode",
		len(e.Paths), strings.Join(e.Paths, ", "))
}

// Discover returns the hooks to execute, in execution order: the tracked store
// first, then the local one, each in Rel order. It is the
// execution-eligibility layer over Enumerate, and the only place that decides
// what "eligible" means.
//
// Two states are refusals rather than filters -- a hook left in the legacy
// location, and a discovered hook that is not executable -- and each comes back
// as a typed error so a caller can map it to an exit code. The non-executable
// refusal is store-independent: the live store and the checkout-provided one
// answer a missing execute bit the same way, in their own typed errors because
// the remedies differ by a commit, and never as a silent skip.
//
// What this returns is a list of scripts the caller will EXECUTE, drawn partly
// from the checkout's own content -- see Origin for the boundary that governs
// when that content runs.
func Discover(s Store) ([]string, error) {
	all, err := Enumerate(s)
	if err != nil {
		return nil, err
	}

	var legacy, trackedNonExec, localNonExec []string
	var hooks []string
	for _, loc := range all {
		if loc.Origin == OriginLegacy {
			legacy = append(legacy, loc.Path)
			continue
		}
		if !loc.IsHookName() {
			continue
		}
		if loc.Executable {
			hooks = append(hooks, loc.Path)
			continue
		}
		if loc.Origin == OriginTracked {
			trackedNonExec = append(trackedNonExec, loc.Path)
			continue
		}
		localNonExec = append(localNonExec, loc.Path)
	}

	if len(legacy) > 0 {
		return nil, &LegacyLocationError{Paths: legacy}
	}
	if len(trackedNonExec) > 0 {
		return nil, &TrackedNotExecutableError{Paths: trackedNonExec}
	}
	if len(localNonExec) > 0 {
		return nil, &LocalNotExecutableError{Paths: localNonExec}
	}
	return hooks, nil
}

// DiscoverMulti discovers hooks across several repositories, concatenating the
// results in order: the first store's hooks run first. It is what a push from a
// submodule uses to run the parent's hooks before its own.
//
// The parameter is a store per repository rather than a git directory, because
// each repository's tracked hooks live in its WORK TREE -- a cascade keyed on
// git directories alone could never see them.
func DiscoverMulti(stores []Store) ([]string, error) {
	var all []string
	for _, s := range stores {
		found, err := Discover(s)
		if err != nil {
			return nil, fmt.Errorf("discovering hooks in %s: %w", s.SharedGitDir, err)
		}
		all = append(all, found...)
	}
	return all, nil
}

// Run executes all discovered hooks sequentially with the given stdin.
// The first failed run (see HookResult.Failed) skips the remaining hooks.
func Run(ctx context.Context, s Store, stdin []byte, timeoutSec int, stopCap time.Duration, env []string, warn func(string)) ([]HookResult, error) {
	hooks, err := Discover(s)
	if err != nil {
		return nil, err
	}
	return RunAll(ctx, hooks, stdin, timeoutSec, stopCap, env, warn)
}

// RunAll executes the given hook paths sequentially with the given stdin.
// The first failed run (see HookResult.Failed) skips the remaining hooks. The
// error is safegit's own -- a hook it could not contain -- never a hook's
// verdict, which is in the results. warn receives safegit's own lines about a
// run: the announcement that an interrupted hook is being stopped, and a stop
// that reached its cap while output was still arriving.
func RunAll(ctx context.Context, hookPaths []string, stdin []byte, timeoutSec int, stopCap time.Duration, env []string, warn func(string)) ([]HookResult, error) {
	if len(hookPaths) == 0 {
		return nil, nil
	}

	var results []HookResult
	for _, hookPath := range hookPaths {
		if ctx.Err() != nil {
			// Cancelled between two hooks: the next one is not started.
			break
		}
		result, err := runOne(ctx, hookPath, stdin, timeoutSec, stopCap, env, warn)
		if err != nil {
			return results, err
		}
		results = append(results, result)
		if result.Failed() {
			break // abort on first failure
		}
	}
	return results, nil
}

// RunSingle executes a single hook by path.
func RunSingle(ctx context.Context, hookPath string, stdin []byte, timeoutSec int, stopCap time.Duration, env []string, warn func(string)) (HookResult, error) {
	return runOne(ctx, hookPath, stdin, timeoutSec, stopCap, env, warn)
}

// The stop of a hook and the processes it started -- on timeout, on
// interruption, and for what a hook left behind when it ended -- runs under one
// hard cap, measured from the moment the stop begins. Every process gets
// SIGTERM at once and the SIGTERM grace to shut down cleanly; what is still
// alive then gets SIGKILL and killWindow to die and be reaped; and the output
// still arriving gets readGrace. A process alive at the end is named as not
// stoppable and left: a process in uninterruptible sleep outlives SIGKILL, and
// waiting for it would make the cap a lie.
const (
	// DefaultStopCap is the cap when the command was given none.
	DefaultStopCap = 60 * time.Second
	// MaxStopCap is the largest cap a command may be given.
	MaxStopCap = 1800 * time.Second
	// MinStopCap is the smallest: the fixed end of the stop plus a SIGTERM
	// grace no shorter than minTermGrace.
	MinStopCap = StopWindow + minTermGrace

	// killWindow is how long processes get to die of SIGKILL and be reaped.
	killWindow = 5 * time.Second
	// readGrace bounds how long a read of a hook's output may wait for data
	// once every process safegit could find has been stopped. A pipe that stays
	// open and silent that long is held by a process safegit could not reach,
	// and the read ends are closed so that safegit can never hang on it. Only
	// waiting for data counts: a destination that takes its time over the
	// output already read never trips it before the cap.
	readGrace = time.Second
	// StopWindow is the fixed end of every stop, after the SIGTERM grace:
	// killWindow, then readGrace. The SIGTERM grace is the cap minus this.
	StopWindow = killWindow + readGrace
	// minTermGrace is the shortest SIGTERM grace a cap may leave: the grace
	// every stop gave before the cap existed.
	minTermGrace = 5 * time.Second
)

// StopCapFromSeconds returns the cap a command was given in whole seconds. A
// value outside MinStopCap to MaxStopCap is an error naming the range: a cap
// safegit cannot honor is refused rather than replaced by one it can. The
// caller names where the value came from.
func StopCapFromSeconds(secs int) (time.Duration, error) {
	if secs < int(MinStopCap/time.Second) || secs > int(MaxStopCap/time.Second) {
		return 0, fmt.Errorf("the cap on stopping a hook and the processes it started must be a whole number of seconds from %d to %d",
			int(MinStopCap/time.Second), int(MaxStopCap/time.Second))
	}
	return time.Duration(secs) * time.Second, nil
}

// FormatStopCap words a cap in whole seconds, as a command is given it: "60s".
func FormatStopCap(limit time.Duration) string {
	return strconv.Itoa(int(limit/time.Second)) + "s"
}

// stopClock is one stop's deadlines, fixed when the stop begins.
type stopClock struct {
	// term is when the SIGTERM grace ends and SIGKILL is sent.
	term time.Time
	// kill is when the SIGKILL window ends: a process alive then is not
	// stoppable.
	kill time.Time
	// end is the cap: reading the hook's output ends here at the latest.
	end time.Time
}

func startStop(limit time.Duration) stopClock {
	now := time.Now()
	return stopClock{
		term: now.Add(limit - StopWindow),
		kill: now.Add(limit - readGrace),
		end:  now.Add(limit),
	}
}

// runOne executes a single hook, forwarding its stdout and stderr to the
// package-level writers, and enforces two rules on it.
//
// The configured timeout is the only budget. On timeout, and when ctx is
// cancelled, the hook is stopped under stopCap: its process group and every
// process it started get SIGTERM, and SIGKILL once the SIGTERM grace ends.
//
// A hook that leaves any process running when it ends -- normally or by the
// timeout -- is a failed run. Once the hook process itself has exited, the
// platform's containment (contain_linux.go, contain_other.go) finds what it
// left behind, stops it where it can, and names it in Leftovers.
//
// The hook's three standard streams are pipes safegit creates itself and hands
// to the child as files, so os/exec starts no copying goroutine of its own and
// Wait returns when the hook process exits, whoever else still holds a pipe.
// Nothing a hook writes is inspected, and nothing a hook writes can change the
// budget it runs under: a hook that needs a different timeout reads
// SAFEGIT_HOOK_TIMEOUT_S from its environment and is configured, never
// self-declared on stdout.
func runOne(ctx context.Context, hookPath string, stdin []byte, timeoutSec int, stopCap time.Duration, env []string, warn func(string)) (HookResult, error) {
	name := filepath.Base(hookPath)
	start := time.Now()
	result := HookResult{Name: name}

	c, err := beginContainment()
	if err != nil {
		return result, fmt.Errorf("hook %s: %w", name, err)
	}
	defer c.end()

	p, err := newHookPipes()
	if err != nil {
		return result, fmt.Errorf("hook %s: %w", name, err)
	}

	cmd := exec.Command(hookPath)
	cmd.Stdin = p.inR
	cmd.Stdout = p.outW
	cmd.Stderr = p.errW
	cmd.Dir = filepath.Dir(hookPath)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	// Set process group so we can signal the entire group (Unix only)
	setProcGroup(cmd)

	if err := cmd.Start(); err != nil {
		p.closeAll()
		result.StartError = startError(err)
		result.Duration = time.Since(start)
		return result, nil
	}
	p.closeChildEnds()

	if err := c.started(cmd.Process.Pid); err != nil {
		killGroup(cmd, syscall.SIGKILL)
		cmd.Wait()
		p.closeParentEnds()
		return result, fmt.Errorf("hook %s: %w", name, err)
	}

	// stdin is written by a goroutine nobody waits on: a hook that never reads
	// it, while a process it left behind holds the read end, must not stall
	// safegit. Closing the write end below unblocks it.
	go func() {
		p.inW.Write(stdin)
		p.inW.Close()
	}()

	fwd := p.forward(stdout, stderr)

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	var waitErr error
	signalled := false
	// hookAlive is set when the hook itself outlived SIGKILL until the cap.
	hookAlive := false
	var clock stopClock
	// stop ends the hook from outside: SIGTERM to the whole process group and
	// to every process the hook started outside it, so each gets the whole
	// SIGTERM grace; then SIGKILL to the group. The hook is not reaped before
	// the signals, so its pid -- the group id -- cannot have been reused. A hook
	// still alive when the SIGKILL window ends is left, and named.
	stop := func() {
		signalled = true
		clock = startStop(stopCap)
		killGroup(cmd, syscall.SIGTERM)
		c.terminate()
		select {
		case waitErr = <-waited:
			return
		case <-time.After(time.Until(clock.term)):
		}
		killGroup(cmd, syscall.SIGKILL)
		select {
		case waitErr = <-waited:
		case <-time.After(time.Until(clock.kill)):
			hookAlive = true
		}
	}
	timer := time.NewTimer(time.Duration(timeoutSec) * time.Second)
	defer timer.Stop()
	select {
	case waitErr = <-waited:
	case <-timer.C:
		result.TimedOut = true
		stop()
	case <-ctx.Done():
		// A cancellation -- safegit itself was interrupted -- stops the hook
		// the same way the timeout does, and the sweep below stops what it left.
		// The stop can take a while and a second interruption is ignored
		// meanwhile, so the operator is told what is happening and for how long
		// at most.
		warn(fmt.Sprintf("stopping hook %s and the processes it started; this can take up to %s", name, FormatStopCap(stopCap)))
		stop()
	}
	result.stopCap = stopCap

	// The hook has exited and is reaped, unless it outlived SIGKILL. Whatever
	// it started that is still running is left behind, and stopping it is a
	// stop too: when the hook ended by itself, the cap runs from here.
	if !signalled {
		clock = startStop(stopCap)
	}
	result.Leftovers, result.LeftoverUnknown = c.sweep(clock, signalled)
	if hookAlive {
		pid := cmd.Process.Pid
		comm, state := describe(pid)
		if comm == "" {
			comm = name
		}
		result.Leftovers = append([]LeftoverProcess{{PID: pid, Command: comm, State: state, FoundState: state, Hook: true}}, result.Leftovers...)
	}
	unstopped := false
	for _, l := range result.Leftovers {
		if !l.Killed {
			unstopped = true
		}
	}

	// Every process safegit could find is stopped, so the output ends once
	// what is left in the pipes is read -- unless something it could not reach
	// still holds a pipe, which shows as a read waiting for data that never
	// comes, or the cap arrives first. A process named above as still running
	// may be what holds it, so no other holder is looked for then.
	swept := time.Now()
	for waiting := true; waiting; {
		select {
		case <-fwd.done:
			waiting = false
		case <-time.After(20 * time.Millisecond):
			starved := fwd.starved(readGrace, swept)
			if !starved && time.Now().Before(clock.end) {
				continue
			}
			switch {
			case unstopped:
			case starved:
				holders, unknown := c.outputHolders(p.readFds())
				result.Leftovers = append(result.Leftovers, holders...)
				if unknown != "" && result.LeftoverUnknown == "" {
					result.LeftoverUnknown = unknown
				}
			default:
				warn(fmt.Sprintf("hook %s: its output was still arriving when the stop reached its %s cap, and safegit stopped reading it", name, FormatStopCap(stopCap)))
			}
			p.closeParentEnds()
			<-fwd.done
			waiting = false
		}
	}
	p.inW.Close()
	p.closeParentEnds()

	result.Duration = time.Since(start)
	// ExitCode is the HOOK's own status, never safegit's. A hook safegit
	// killed -- the timeout, or a cancellation -- has none, so it stays nil;
	// TimedOut is what decides the caller's exit code for a timeout.
	if !signalled {
		code := 0
		if waitErr != nil {
			code = 1
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				code = exitErr.ExitCode()
			}
		}
		result.ExitCode = &code
	}
	return result, nil
}

// startError words why exec refused a hook: the system's reason, without the
// path, which the hook's name already carries -- "exec: permission denied",
// "exec: no such file or directory" (the file, or the interpreter its #! line
// names, is missing), "exec: exec format error".
func startError(err error) string {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return "exec: " + pathErr.Err.Error()
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return "exec: " + execErr.Err.Error()
	}
	return "exec: " + err.Error()
}

// hookPipes are the three pipes a hook's standard streams run over. The child
// ends are handed to the hook as files; the parent ends are safegit's.
type hookPipes struct {
	inR, inW   *os.File
	outR, outW *os.File
	errR, errW *os.File
}

func newHookPipes() (*hookPipes, error) {
	var p hookPipes
	var err error
	if p.inR, p.inW, err = os.Pipe(); err != nil {
		return nil, fmt.Errorf("creating the stdin pipe: %w", err)
	}
	if p.outR, p.outW, err = os.Pipe(); err != nil {
		p.closeAll()
		return nil, fmt.Errorf("creating the stdout pipe: %w", err)
	}
	if p.errR, p.errW, err = os.Pipe(); err != nil {
		p.closeAll()
		return nil, fmt.Errorf("creating the stderr pipe: %w", err)
	}
	return &p, nil
}

// closeChildEnds closes safegit's copies of the ends the hook now holds, so
// that the hook's exit (and its descendants') is what ends the output.
func (p *hookPipes) closeChildEnds() {
	for _, f := range []*os.File{p.inR, p.outW, p.errW} {
		if f != nil {
			f.Close()
		}
	}
}

// closeParentEnds closes safegit's own ends; a read or write blocked on one
// returns. Closing twice is harmless.
func (p *hookPipes) closeParentEnds() {
	for _, f := range []*os.File{p.inW, p.outR, p.errR} {
		if f != nil {
			f.Close()
		}
	}
}

func (p *hookPipes) closeAll() {
	p.closeChildEnds()
	p.closeParentEnds()
}

// forward copies the hook's stdout and stderr to the given writers; done is
// closed once both have ended.
func (p *hookPipes) forward(out, errw io.Writer) *forwarding {
	f := &forwarding{done: make(chan struct{})}
	outStream, errStream := &stream{}, &stream{}
	f.streams = []*stream{outStream, errStream}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		beforeStdoutRead()
		outStream.copy(out, p.outR)
	}()
	go func() {
		defer wg.Done()
		errStream.copy(errw, p.errR)
	}()
	go func() {
		wg.Wait()
		close(f.done)
	}()
	return f
}

// forwarding is the copying of a hook's two output streams.
type forwarding struct {
	done    chan struct{}
	streams []*stream
}

// starved reports whether a stream has been waiting for data for at least d,
// counting from no earlier than since: a wait that began while the hook was
// still running and silent says nothing about who holds the pipe now.
func (f *forwarding) starved(d time.Duration, since time.Time) bool {
	for _, s := range f.streams {
		if s.waitingFor(since) >= d {
			return true
		}
	}
	return false
}

// stream copies one pipe and records when it is blocked waiting for data, as
// opposed to handing data to a destination that is slow to take it.
type stream struct {
	mu           sync.Mutex
	waitingSince time.Time
}

func (s *stream) waitingFor(since time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.waitingSince.IsZero() {
		return 0
	}
	if s.waitingSince.After(since) {
		since = s.waitingSince
	}
	return time.Since(since)
}

func (s *stream) setWaiting(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if on {
		s.waitingSince = time.Now()
	} else {
		s.waitingSince = time.Time{}
	}
}

// copy reads r to its end and writes what it reads to w. After a write error
// it keeps reading and discards, so that the hook is never blocked on a full
// pipe nobody drains.
func (s *stream) copy(w io.Writer, r io.Reader) {
	buf := make([]byte, 32*1024)
	writeFailed := false
	for {
		s.setWaiting(true)
		n, err := r.Read(buf)
		s.setWaiting(false)
		if n > 0 && !writeFailed {
			if _, werr := w.Write(buf[:n]); werr != nil {
				writeFailed = true
			}
		}
		if err != nil {
			return
		}
	}
}

// readFds returns the descriptor numbers of safegit's read ends of the hook's
// stdout and stderr pipes. It goes through SyscallConn rather than Fd, which
// would switch the descriptors to blocking mode and stop a Close from
// unblocking a read.
func (p *hookPipes) readFds() []int {
	var fds []int
	for _, f := range []*os.File{p.outR, p.errR} {
		rc, err := f.SyscallConn()
		if err != nil {
			continue
		}
		rc.Control(func(fd uintptr) { fds = append(fds, int(fd)) })
	}
	return fds
}

// isExecutable checks if a file has any execute permission bit set.
func isExecutable(info os.FileInfo) bool {
	return info.Mode()&0111 != 0
}

// PlanInstall reads the hook source and resolves the destination inside the
// tool-owned live store under the shared git dir, without mutating anything.
// Callers mint the mkdir/write/chmod themselves so a dry run can record the
// install instead of performing it.
//
// An existing destination is REFUSED rather than overwritten: an install that
// silently replaced a hook could destroy the operator's own script (and, back
// when the store was git's own .git/hooks, a native git hook safegit itself
// runs). Upgrading a hook is `safegit hook remove <name>` followed by an
// install, which says out loud that the old one is going away.
func PlanInstall(sharedGitDir, srcPath string) (data []byte, dest string, err error) {
	data, err = os.ReadFile(srcPath)
	if err != nil {
		return nil, "", fmt.Errorf("reading hook file: %w", err)
	}
	dest = filepath.Join(LocalDir(sharedGitDir), filepath.Base(srcPath))
	if _, statErr := os.Lstat(dest); statErr == nil {
		return nil, "", fmt.Errorf("%s already exists; remove it first (`safegit hook remove %s`) -- install never overwrites a hook",
			dest, filepath.Base(srcPath))
	}
	return data, dest, nil
}
