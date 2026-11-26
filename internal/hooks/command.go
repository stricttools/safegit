package hooks

import (
	"context"
	"os"
	"os/exec"
)

// Executable reports platform-native execution permission. Windows enforces
// FILE_EXECUTE, including for scripts; Unix enforces the POSIX execute bits.
func Executable(path string, info os.FileInfo) bool { return isExecutable(path, info) }

// CommandContext builds an interpreter-aware native hook command. Its caller
// owns its streams, environment, working directory and containment policy.
func CommandContext(ctx context.Context, path string, args ...string) (*exec.Cmd, error) {
	cmd, err := hookCommand(path)
	if err != nil {
		return nil, err
	}
	argv := append(append([]string(nil), cmd.Args[1:]...), args...)
	return exec.CommandContext(ctx, cmd.Path, argv...), nil
}
