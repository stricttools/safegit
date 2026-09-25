package hooks

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// The helpers here serve the containment that has no subreaper to rely on
// (contain_other.go). They are platform-independent so that their tests run on
// every platform: they parse tool output handed to them, and run nothing.

// pipeHolders names the processes, other than safegit (self), holding the
// write end of any of the pipes whose read ends safegit holds as readFds. It
// reads `lsof -F` output from the lsof function.
//
// lsof reports each end of a pipe with its own kernel address as the device and
// the peer end's address as the name ("->0x..."). safegit's own read end names
// the write end, so a holder is any other process with an open file whose
// device is that address, or whose name points back at the read end.
//
// On failure no holder is returned and the reason is: a missing lsof, a failing
// one, or output that does not list safegit's own read end.
func pipeHolders(lsof func() ([]byte, error), self int, readFds []int) ([]LeftoverProcess, string) {
	out, err := lsof()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Sprintf("lsof, which names them, was not found: %v", err)
		}
		return nil, fmt.Sprintf("lsof, which names them, failed: %v", err)
	}
	files := parseLsof(out)

	wanted := map[string]bool{}
	for _, fd := range readFds {
		wanted[strconv.Itoa(fd)] = true
	}
	writeEnds := map[string]bool{}
	readEnds := map[string]bool{}
	for _, f := range files {
		if f.pid != self || !wanted[f.fd] {
			continue
		}
		if peer, ok := strings.CutPrefix(f.name, "->"); ok && peer != "" {
			writeEnds[peer] = true
		}
		if f.device != "" {
			readEnds[f.device] = true
		}
	}
	if len(writeEnds) == 0 && len(readEnds) == 0 {
		return nil, fmt.Sprintf("lsof did not list safegit's own read end of the hook's output (pid %d, descriptors %v)", self, readFds)
	}

	var holders []LeftoverProcess
	seen := map[int]bool{}
	for _, f := range files {
		if f.pid == self || seen[f.pid] {
			continue
		}
		peer, _ := strings.CutPrefix(f.name, "->")
		if writeEnds[f.device] || (peer != "" && readEnds[peer]) {
			seen[f.pid] = true
			holders = append(holders, LeftoverProcess{PID: f.pid, Command: f.command})
		}
	}
	sort.Slice(holders, func(i, j int) bool { return holders[i].PID < holders[j].PID })
	return holders, ""
}

// lsofFile is one open file from `lsof -F` output, with its process.
type lsofFile struct {
	pid     int
	command string
	fd      string
	device  string
	name    string
}

// parseLsof reads `lsof -F` field output: a 'p' line starts a process, a 'c'
// line names it, an 'f' line starts one of its files, and 'd'/'D' (device) and
// 'n' (name) describe that file. Other fields are ignored.
func parseLsof(out []byte) []lsofFile {
	var files []lsofFile
	var pid int
	var command string
	cur := -1
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		field, value := line[0], line[1:]
		switch field {
		case 'p':
			pid, _ = strconv.Atoi(value)
			command = ""
			cur = -1
		case 'c':
			command = value
		case 'f':
			files = append(files, lsofFile{pid: pid, command: command, fd: value})
			cur = len(files) - 1
		case 'd', 'D':
			if cur >= 0 && strings.HasPrefix(value, "0x") {
				files[cur].device = value
			}
		case 'n':
			if cur >= 0 {
				files[cur].name = value
			}
		}
	}
	return files
}

// parsePsGroup reads `ps -A -o pid= -o pgid= -o comm=` output and returns the
// processes in process group pgid, named by the base name of their command.
func parsePsGroup(out []byte, pgid int) []LeftoverProcess {
	var members []LeftoverProcess
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		group, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil || group != pgid {
			continue
		}
		command := filepath.Base(strings.Join(fields[2:], " "))
		members = append(members, LeftoverProcess{PID: pid, Command: command})
	}
	return members
}
