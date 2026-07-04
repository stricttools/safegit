//go:build windows

package filelock

import "os"

// No-op flock: oplog writes are < 4096 bytes (POSIX O_APPEND atomic),
// scrub policies are serialized by coordination lock.

func lockFile(_ *os.File) error   { return nil }
func unlockFile(_ *os.File) error { return nil }
