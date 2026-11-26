package testutil

import (
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// ShellPath quotes a native fixture path for a POSIX/Git-for-Windows shell.
// Never put an unquoted C:\path into script text: backslash is shell syntax.
func ShellPath(path string) string {
	return "'" + strings.ReplaceAll(filepath.ToSlash(path), "'", "'\\''") + "'"
}

// FixtureScript translates literal native drive paths embedded by legacy
// shell fixtures. Backslash escapes elsewhere (printf, regex, quoting) remain
// untouched. This is fixture-only; user hook source is never rewritten.
func FixtureScript(script string) string {
	if runtime.GOOS != "windows" {
		return script
	}
	return nativeDrivePath.ReplaceAllStringFunc(script, func(p string) string { return strings.ReplaceAll(p, "\\", "/") })
}

var nativeDrivePath = regexp.MustCompile(`[A-Za-z]:\\[^\s'"<>|;]*`)
