//go:build !windows

package lock

import (
	"os"
	"syscall"
)

func lockReclaim(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
func openReclaim(path string) (*os.File, error)     { return os.Open(path) }
func statIdentity(path string) (os.FileInfo, error) { return os.Stat(path) }

func readReclaimFields(f *os.File) (map[string]string, error) { return readLockFields(f) }
