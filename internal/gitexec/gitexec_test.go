package gitexec

import (
	"context"
	"strings"
	"testing"
)

func TestCommandPrefixesEveryInvocation(t *testing.T) {
	cmd, err := Command(context.Background(), Spec{Args: []string{"status", "--porcelain"}})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	want := []string{Binary, "--no-optional-locks", "status", "--porcelain"}
	if got := cmd.Args; !equalStrings(got, want) {
		t.Errorf("argv = %v, want %v", got, want)
	}
}

func TestCommandRefusesUndeclaredSubcommand(t *testing.T) {
	_, err := Command(context.Background(), Spec{Args: []string{"filter-branch", "--all"}})
	if err == nil {
		t.Fatal("Command accepted a subcommand the classification table does not declare")
	}
	if !strings.Contains(err.Error(), "filter-branch") {
		t.Errorf("refusal should name the subcommand; got %v", err)
	}
}

func TestCommandRefusesArgvWithNoSubcommand(t *testing.T) {
	if _, err := Command(context.Background(), Spec{Args: nil}); err == nil {
		t.Fatal("Command accepted an empty argv")
	}
}

func TestRootPinSetsWorkingDirectory(t *testing.T) {
	ctx := WithRoot(context.Background(), "/repo/root")
	cmd, err := Command(ctx, Spec{Args: []string{"ls-files"}})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if cmd.Dir != "/repo/root" {
		t.Errorf("cmd.Dir = %q, want the pinned repository root", cmd.Dir)
	}
}

func TestEmptyRootIsNoPin(t *testing.T) {
	ctx := WithRoot(context.Background(), "")
	if _, ok := Root(ctx); ok {
		t.Error("an empty root must carry no pin at all")
	}
	cmd, err := Command(ctx, Spec{Args: []string{"ls-files"}})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if cmd.Dir != "" {
		t.Errorf("cmd.Dir = %q, want the process working directory", cmd.Dir)
	}
}

func TestDeclaredExemptionSuspendsTheRootPin(t *testing.T) {
	ctx := WithoutRootPin(WithRoot(context.Background(), "/repo/root"), ExemptGuardedPassthrough)
	cmd, err := Command(ctx, Spec{Args: []string{"cherry-pick", "abc123"}})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if cmd.Dir != "" {
		t.Errorf("cmd.Dir = %q, want the operator's own working directory", cmd.Dir)
	}
}

func TestWithoutRootPinRefusesAnUndeclaredExemption(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("WithoutRootPin accepted an exemption the table does not declare")
		}
	}()
	WithoutRootPin(context.Background(), ExemptionID("main.somethingInvented"))
}

func TestWithoutRootPinRefusesAWrongKind(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("WithoutRootPin accepted an explicit-directory exemption as an operator-cwd one")
		}
	}()
	WithoutRootPin(context.Background(), ExemptRunWithGitDir)
}

func TestExplicitDirectorySupersedesTheRootPin(t *testing.T) {
	ctx := WithRoot(context.Background(), "/repo/root")
	cmd, err := Command(ctx, Spec{
		Args:     []string{"rev-list", "--all"},
		Exempt:   ExemptRunWithGitDir,
		GitDir:   "/other/.git",
		WorkTree: "/other",
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if cmd.Dir != "/other" {
		t.Errorf("cmd.Dir = %q, want the explicitly targeted work tree", cmd.Dir)
	}
	assertEnv(t, cmd.Env, "GIT_DIR=/other/.git")
	assertEnv(t, cmd.Env, "GIT_WORK_TREE=/other")
}

func TestContextDirOverrideSupersedesTheRootPin(t *testing.T) {
	ctx := WithDir(WithRoot(context.Background(), "/repo/root"), "/sub/.git", "/sub")
	cmd, err := Command(ctx, Spec{Args: []string{"rev-list", "HEAD"}})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if cmd.Dir != "/sub" {
		t.Errorf("cmd.Dir = %q, want the context-targeted work tree", cmd.Dir)
	}
	assertEnv(t, cmd.Env, "GIT_DIR=/sub/.git")
}

func TestExplicitDirectoryRequiresADeclaredExemption(t *testing.T) {
	_, err := Command(context.Background(), Spec{Args: []string{"rev-list", "--all"}, GitDir: "/other/.git"})
	if err == nil {
		t.Fatal("a spec set its own git directory without naming a declared exemption")
	}
	if !strings.Contains(err.Error(), "exemption table") {
		t.Errorf("refusal should point at the exemption table; got %v", err)
	}
}

func TestExtraEnvIsAppendedToTheProcessEnvironment(t *testing.T) {
	cmd, err := Command(context.Background(), Spec{
		Args: []string{"write-tree"},
		Env:  []string{"GIT_INDEX_FILE=/tmp/idx"},
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	assertEnv(t, cmd.Env, "GIT_INDEX_FILE=/tmp/idx")
	if len(cmd.Env) < 2 {
		t.Errorf("the subprocess environment must extend the process one, got %v", cmd.Env)
	}
}

// TestEnvCannotSmuggleADirectoryOverride: the exemption pairing inspects the
// GitDir, WorkTree and Dir fields, so a directory override spelled as an
// environment entry would reach another repository with nothing declared. The
// boundary refuses all three spellings.
func TestEnvCannotSmuggleADirectoryOverride(t *testing.T) {
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR"} {
		_, err := Command(context.Background(), Spec{
			Args: []string{"rev-parse", "--git-common-dir"},
			Env:  []string{name + "=/other/.git"},
		})
		if err == nil {
			t.Errorf("Command accepted %s in Spec.Env", name)
			continue
		}
		if !strings.Contains(err.Error(), name) {
			t.Errorf("refusal should name %s; got %v", name, err)
		}
		if !strings.Contains(err.Error(), "exemption table") {
			t.Errorf("refusal should point at the declared fields and the exemption table; got %v", err)
		}
	}
}

// TestEnvDirectoryRefusalDoesNotCatchLookalikes: the refusal is on the variable
// NAME, so a variable that merely starts with one of the banned names (or
// carries one in its value) is untouched.
func TestEnvDirectoryRefusalDoesNotCatchLookalikes(t *testing.T) {
	cmd, err := Command(context.Background(), Spec{
		Args: []string{"write-tree"},
		Env:  []string{"GIT_DIR_SUFFIX=/other/.git", "GIT_INDEX_FILE=/idx/GIT_DIR=x"},
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	assertEnv(t, cmd.Env, "GIT_DIR_SUFFIX=/other/.git")
	assertEnv(t, cmd.Env, "GIT_INDEX_FILE=/idx/GIT_DIR=x")
}

// TestExemptSpecStillGetsTheCommonEnvironmentTail is the property the comment
// above the environment assembly states: exemption from the DIRECTORY pin is
// exemption from nothing else. A spec that targets its own repository must
// still receive every other override the boundary assembles -- today the extra
// environment entries, tomorrow whatever else travels the same way -- so the
// explicit-directory branch must not short-circuit the tail.
func TestExemptSpecStillGetsTheCommonEnvironmentTail(t *testing.T) {
	ctx := WithRoot(context.Background(), "/repo/root")
	cmd, err := Command(ctx, Spec{
		Args:     []string{"rev-list", "--all"},
		Exempt:   ExemptRunWithGitDir,
		GitDir:   "/other/.git",
		WorkTree: "/other",
		Env:      []string{"GIT_INDEX_FILE=/idx/tmp", "GIT_TERMINAL_PROMPT=0"},
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	// The directory targeting the exemption covers...
	assertEnv(t, cmd.Env, "GIT_DIR=/other/.git")
	assertEnv(t, cmd.Env, "GIT_WORK_TREE=/other")
	if cmd.Dir != "/other" {
		t.Errorf("cmd.Dir = %q, want the explicitly targeted work tree", cmd.Dir)
	}
	// ...and the spec's own environment, which it does not.
	assertEnv(t, cmd.Env, "GIT_INDEX_FILE=/idx/tmp")
	assertEnv(t, cmd.Env, "GIT_TERMINAL_PROMPT=0")
	if len(cmd.Env) <= 4 {
		t.Errorf("the subprocess environment must extend the process one, got %v", cmd.Env)
	}
}

func TestArgvAnyCarriesBinaryAndPrefix(t *testing.T) {
	argv, err := ArgvAny(ExemptGitMutation, "checkout", "other")
	if err != nil {
		t.Fatalf("ArgvAny: %v", err)
	}
	want := []interface{}{Binary, "--no-optional-locks", "checkout", "other"}
	if len(argv) != len(want) {
		t.Fatalf("argv = %v, want %v", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv = %v, want %v", argv, want)
		}
	}
}

func TestArgvAnyRefusesUndeclaredExemptionAndSubcommand(t *testing.T) {
	if _, err := ArgvAny(ExemptionID("main.invented"), "checkout"); err == nil {
		t.Error("ArgvAny accepted an undeclared exemption")
	}
	if _, err := ArgvAny(ExemptGitMutation, "filter-branch"); err == nil {
		t.Error("ArgvAny accepted an undeclared subcommand")
	}
}

func TestGlobalPrefixIsACopy(t *testing.T) {
	p := GlobalPrefix()
	p[0] = "--tampered"
	if GlobalPrefix()[0] != "--no-optional-locks" {
		t.Error("GlobalPrefix handed out the package's own slice")
	}
}

func assertEnv(t *testing.T, env []string, want string) {
	t.Helper()
	for _, e := range env {
		if e == want {
			return
		}
	}
	t.Errorf("environment does not carry %q", want)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
