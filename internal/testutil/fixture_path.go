package testutil

import (
	"runtime"
	"strings"
)

// FixturePath keeps Unix's byte-path fixtures unchanged. Windows uses distinct
// legal Unicode equivalents: these still exercise quoting/Unicode/whitespace,
// but do not pretend NTFS can materialize POSIX-only names. Pure codec tests
// retain the original byte strings on every platform.
func FixturePath(path string) string {
	if runtime.GOOS != "windows" {
		return path
	}
	path = strings.NewReplacer("\\", "＼", "\"", "＂", ":", "꞉", "*", "＊", "?", "？", "<", "＜", ">", "＞", "|", "｜", "\t", "␉", "\n", "␊", "\r", "␍").Replace(path)
	if strings.HasSuffix(path, ".") {
		path = strings.TrimSuffix(path, ".") + "．"
	}
	return path
}
