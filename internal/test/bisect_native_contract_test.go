package test

import (
	"fmt"
	"github.com/stricttools/safegit/internal/testutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise every word the classified bisect vocabulary declares, not just help
// and dirty-tree refusals. Programs/remotes are local native fixtures only.
func TestBisectDeclaredSubcommandsNative(t *testing.T) {
	for _, sub := range []string{"start", "good", "bad", "old", "new", "skip", "run", "replay", "reset", "terms", "log", "view"} {
		t.Run(sub, func(t *testing.T) {
			dir := newRepo(t)
			for i := 0; i < 4; i++ {
				testutil.WriteFile(t, dir, "bisect.txt", fmt.Sprintf("%d\n", i))
				testutil.Git(t, dir, "add", "bisect.txt")
				testutil.Git(t, dir, "commit", "-q", "-m", fmt.Sprintf("point %d", i))
			}
			tip := testutil.Rev(t, dir, "HEAD")
			good := testutil.Rev(t, dir, "HEAD~4")
			argv := []string{"bisect", sub}
			viewMarker := ""
			var env []string
			if sub == "start" {
				argv = append(argv, tip, good)
			} else {
				setup := []string{"bisect", "start"}
				if sub == "old" || sub == "new" {
					setup = append(setup, "--term-old=old", "--term-new=new")
				}
				setup = append(setup, tip, good)
				testutil.Git(t, dir, setup...)
			}
			switch sub {
			case "view":
				// On headless Unix Git otherwise selects `git log` instead of
				// invoking gitk. Our viewer is a non-GUI fixture on every OS.
				env = append(env, "DISPLAY=:safegit-test")
				shim := t.TempDir()
				env = append(env, "GIT_EXEC_PATH="+shim)
				viewMarker = filepath.Join(t.TempDir(), "viewer-ran")
				testutil.WriteExecutable(t, filepath.Join(shim, "gitk"), "#!/bin/sh\nprintf '%s\\n' \"$*\" > "+testutil.ShellPath(viewMarker)+"\n")
				t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
			case "run":
				argv = append(argv, "git", "version") // a real native executable, no shell shim or GUI
			case "replay":
				log := testutil.GitOut(t, dir, "bisect", "log")
				file := filepath.Join(t.TempDir(), "replay.txt")
				if err := os.WriteFile(file, []byte(log), 0600); err != nil {
					t.Fatal(err)
				}
				testutil.Git(t, dir, "bisect", "reset")
				argv = append(argv, file)
			}
			out, stderr, code := runSafegitEnv(t, dir, env, argv...)
			if code != 0 {
				t.Fatalf("native %v failed: code=%d stdout=%s stderr=%s", argv, code, out, stderr)
			}
			if sub == "view" {
				raw, err := os.ReadFile(viewMarker)
				if err != nil || !strings.Contains(string(raw), "--bisect") {
					t.Fatalf("Git did not invoke the native fixture viewer: %q %v", raw, err)
				}
			}
			if sub == "reset" {
				if head := testutil.Rev(t, dir, "HEAD"); head != tip {
					t.Fatalf("reset left HEAD=%s, want %s", head, tip)
				}
			} else {
				if _, err := os.Stat(filepath.Join(dir, ".git", "BISECT_LOG")); err != nil {
					t.Fatalf("bisect state missing after %s: %v", sub, err)
				}
				if sub == "run" && !strings.Contains(out+stderr, "first bad commit") && !strings.Contains(out+stderr, "first 'bad' commit") {
					t.Fatalf("run did not complete a real bisection: %s %s", out, stderr)
				}
				testutil.Git(t, dir, "bisect", "reset")
			}
		})
	}
}
