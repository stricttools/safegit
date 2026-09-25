// Package hooks discovers and executes pre-pre-push hooks that run before any network I/O, solving the SSH timeout problem when checks are long-running.
package hooks

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/smm-h/safegit/internal/exitcode"
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

// HookResult holds the outcome of running a single hook.
type HookResult struct {
	Name     string        `json:"name"`
	ExitCode int           `json:"exitCode"`
	Duration time.Duration `json:"duration"`
	TimedOut bool          `json:"timedOut,omitempty"`
	// Leftovers are the processes the hook left running when it ended.
	Leftovers []LeftoverProcess `json:"leftover_processes,omitempty"`
	// LeftoverUnknown is non-empty when something the hook started was still
	// holding its output after it ended and safegit could not name it; it says
	// why.
	LeftoverUnknown string `json:"leftover_unknown,omitempty"`
}

// LeftoverProcess is one process a hook left running when it ended.
type LeftoverProcess struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
	// Killed reports that safegit stopped it. False means it is still running.
	Killed bool `json:"killed"`
}

// Failed reports whether the run failed: a nonzero status, a timeout, or any
// process left behind.
func (r HookResult) Failed() bool {
	return r.ExitCode != 0 || r.TimedOut || len(r.Leftovers) > 0 || r.LeftoverUnknown != ""
}

// LeftoverMessages returns the error lines naming what the hook left behind:
// one per process, and one more when something could not be named.
func (r HookResult) LeftoverMessages() []string {
	var msgs []string
	for _, l := range r.Leftovers {
		fate := "it was killed"
		if !l.Killed {
			fate = "it is still running; " + uncontainedNote
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
func Run(ctx context.Context, s Store, stdin []byte, timeoutSec int, env []string) ([]HookResult, error) {
	hooks, err := Discover(s)
	if err != nil {
		return nil, err
	}
	return RunAll(ctx, hooks, stdin, timeoutSec, env)
}

// RunAll executes the given hook paths sequentially with the given stdin.
// The first failed run (see HookResult.Failed) skips the remaining hooks. The
// error is safegit's own -- a hook it could not contain -- never a hook's
// verdict, which is in the results.
func RunAll(ctx context.Context, hookPaths []string, stdin []byte, timeoutSec int, env []string) ([]HookResult, error) {
	if len(hookPaths) == 0 {
		return nil, nil
	}

	var results []HookResult
	for _, hookPath := range hookPaths {
		result, err := runOne(ctx, hookPath, stdin, timeoutSec, env)
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
func RunSingle(ctx context.Context, hookPath string, stdin []byte, timeoutSec int, env []string) (HookResult, error) {
	return runOne(ctx, hookPath, stdin, timeoutSec, env)
}

// killGrace is how long a signalled process gets between SIGTERM and SIGKILL.
const killGrace = 5 * time.Second

// readGrace bounds how long a read of a hook's output may wait for data once
// every process safegit could find has been stopped. A pipe that stays open
// and silent that long is held by a process safegit could not reach, and the
// read ends are closed so that safegit can never hang on it. Only waiting for
// data counts: a destination that takes its time over the output already read
// never trips it, so a slow reader loses nothing.
const readGrace = time.Second

// runOne executes a single hook, forwarding its stdout and stderr to the
// package-level writers, and enforces two rules on it.
//
// The configured timeout is the only budget. On timeout the hook's process
// group gets SIGTERM, then SIGKILL after killGrace.
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
func runOne(ctx context.Context, hookPath string, stdin []byte, timeoutSec int, env []string) (HookResult, error) {
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
		result.ExitCode = 1
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
	timer := time.NewTimer(time.Duration(timeoutSec) * time.Second)
	defer timer.Stop()
	select {
	case waitErr = <-waited:
	case <-timer.C:
		// Timeout: SIGTERM the whole process group, then SIGKILL after the
		// grace. The hook is not reaped before the signals, so its pid -- the
		// group id -- cannot have been reused.
		result.TimedOut = true
		signalled = true
		killGroup(cmd, syscall.SIGTERM)
		select {
		case waitErr = <-waited:
		case <-time.After(killGrace):
			killGroup(cmd, syscall.SIGKILL)
			waitErr = <-waited
		}
	case <-ctx.Done():
		signalled = true
		killGroup(cmd, syscall.SIGKILL)
		waitErr = <-waited
	}

	// The hook has exited and is reaped. Whatever it started that is still
	// running is left behind.
	result.Leftovers, result.LeftoverUnknown = c.sweep(signalled)

	// Every process safegit could find is stopped, so the output ends once
	// what is left in the pipes is read -- unless something it could not reach
	// still holds a pipe, which shows as a read waiting for data that never
	// comes.
	swept := time.Now()
	for waiting := true; waiting; {
		select {
		case <-fwd.done:
			waiting = false
		case <-time.After(20 * time.Millisecond):
			if fwd.starved(readGrace, swept) {
				holders, unknown := c.outputHolders(p.readFds())
				result.Leftovers = append(result.Leftovers, holders...)
				if unknown != "" && result.LeftoverUnknown == "" {
					result.LeftoverUnknown = unknown
				}
				p.closeParentEnds()
				<-fwd.done
				waiting = false
			}
		}
	}
	p.inW.Close()
	p.closeParentEnds()

	result.Duration = time.Since(start)
	switch {
	case result.TimedOut:
		// ExitCode is the HOOK's status, not safegit's. A killed hook has no
		// status of its own, so the marker is deliberately chosen to READ the
		// same as safegit's own hook-timeout code in the "exit=%d" line callers
		// print -- which is why it is taken from the registry rather than
		// written as a bare 21 that duplicates the constant by value. The
		// machine payload does not carry it: a timed-out run's exit_code is
		// null there. Nothing branches on it: TimedOut is what decides the
		// caller's exit code.
		result.ExitCode = exitcode.PushHookTimeout
	case signalled:
		result.ExitCode = 1
	case waitErr != nil:
		result.ExitCode = 1
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		}
	}
	return result, nil
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
