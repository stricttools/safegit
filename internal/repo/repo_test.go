package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSafegitDir(t *testing.T) {
	got := SafegitDir("/foo/.git")
	want := filepath.Join("/foo/.git", "safegit")
	if got != want {
		t.Errorf("SafegitDir = %q, want %q", got, want)
	}
}

func TestInitAndIsInitialized(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	os.MkdirAll(gitDir, 0755)

	if IsInitialized(gitDir) {
		t.Fatal("should not be initialized before Init")
	}

	if err := Init(context.Background(), gitDir); err != nil {
		t.Fatal(err)
	}

	if !IsInitialized(gitDir) {
		t.Fatal("should be initialized after Init")
	}

	// Verify directory structure
	sgDir := SafegitDir(gitDir)
	expectedDirs := []string{
		filepath.Join(sgDir, "locks", "refs", "heads"),
		filepath.Join(sgDir, "tmp"),
	}
	for _, d := range expectedDirs {
		if stat, err := os.Stat(d); err != nil || !stat.IsDir() {
			t.Errorf("expected directory %s to exist", d)
		}
	}

	// Verify config.json
	cfg, err := LoadConfig(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SchemaVersion != 1 {
		t.Errorf("schema version = %d, want 1", cfg.SchemaVersion)
	}
	if cfg.Commit.CASMaxAttempts != 5 {
		t.Errorf("CASMaxAttempts = %d, want 5", cfg.Commit.CASMaxAttempts)
	}

	// Verify log file exists
	logPath := filepath.Join(sgDir, "log")
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		t.Error("log file should exist")
	}
}

// halfInitializedGitDir builds the state that made every safegit command in the
// repository fail: .git/safegit/ exists (here with the tmp/ subdirectory a
// preview index used to leave behind) but config.json does not.
func halfInitializedGitDir(t *testing.T) string {
	t.Helper()
	gitDir := filepath.Join(t.TempDir(), ".git")
	if err := os.MkdirAll(filepath.Join(SafegitDir(gitDir), "tmp"), 0755); err != nil {
		t.Fatalf("manufacturing the half-initialized state: %v", err)
	}
	return gitDir
}

// TestIsInitializedRequiresConfig pins the predicate itself: the safegit
// directory existing is not the question, config.json being present is. Reading
// the bare directory as proof of initialization is what made EnsureInitialized a
// no-op over a half-initialized tree.
func TestIsInitializedRequiresConfig(t *testing.T) {
	gitDir := halfInitializedGitDir(t)

	if IsInitialized(gitDir) {
		t.Error("a safegit dir without config.json must not read as initialized")
	}

	cfg := DefaultConfig()
	if err := SaveConfigTo(ConfigPath(gitDir), &cfg); err != nil {
		t.Fatalf("writing config.json: %v", err)
	}
	if !IsInitialized(gitDir) {
		t.Error("a safegit dir with config.json must read as initialized")
	}
}

// TestInitCompletesHalfInitializedDir: Init must complete a half-initialized
// directory rather than skipping on the strength of the directory existing --
// and must stay idempotent once it has, leaving an existing config alone.
func TestInitCompletesHalfInitializedDir(t *testing.T) {
	gitDir := halfInitializedGitDir(t)

	if err := Init(context.Background(), gitDir); err != nil {
		t.Fatalf("Init over a half-initialized dir: %v", err)
	}
	if !IsInitialized(gitDir) {
		t.Fatal("Init did not complete the half-initialized dir")
	}

	sgDir := SafegitDir(gitDir)
	for _, d := range []string{
		filepath.Join(sgDir, "locks", "refs", "heads"),
		filepath.Join(sgDir, "tmp"),
	} {
		if stat, err := os.Stat(d); err != nil || !stat.IsDir() {
			t.Errorf("expected directory %s to exist after the repair", d)
		}
	}
	if _, err := os.Stat(filepath.Join(sgDir, "log")); err != nil {
		t.Errorf("the repair did not create the log file: %v", err)
	}

	// Idempotent: a second Init must not rewrite an existing config.
	cfg, err := LoadConfig(gitDir)
	if err != nil {
		t.Fatalf("loading the repaired config: %v", err)
	}
	cfg.Push.RetryAttempts = 42
	if err := SaveConfig(gitDir, cfg); err != nil {
		t.Fatalf("saving the edited config: %v", err)
	}
	if err := Init(context.Background(), gitDir); err != nil {
		t.Fatalf("second Init: %v", err)
	}
	reloaded, err := LoadConfig(gitDir)
	if err != nil {
		t.Fatalf("reloading config after the second Init: %v", err)
	}
	if reloaded.Push.RetryAttempts != 42 {
		t.Errorf("a second Init overwrote the existing config: RetryAttempts = %d, want 42",
			reloaded.Push.RetryAttempts)
	}
}

// TestEnsureInitializedCompletesHalfInitializedDir is the same repair through
// the seam every command actually calls.
func TestEnsureInitializedCompletesHalfInitializedDir(t *testing.T) {
	gitDir := halfInitializedGitDir(t)

	if err := EnsureInitialized(context.Background(), gitDir); err != nil {
		t.Fatalf("EnsureInitialized over a half-initialized dir: %v", err)
	}
	if _, err := os.Stat(ConfigPath(gitDir)); err != nil {
		t.Fatalf("the repair did not create config.json: %v", err)
	}
	if _, err := LoadConfig(gitDir); err != nil {
		t.Fatalf("the repaired config is not loadable: %v", err)
	}

	// Idempotent: calling it again on the repaired repo changes nothing.
	if err := EnsureInitialized(context.Background(), gitDir); err != nil {
		t.Fatalf("second EnsureInitialized: %v", err)
	}
	if !IsInitialized(gitDir) {
		t.Error("should still be initialized after a second EnsureInitialized")
	}
}

func TestInitIdempotent(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	os.MkdirAll(gitDir, 0755)

	Init(context.Background(), gitDir)

	// Second init should succeed (idempotent)
	err := Init(context.Background(), gitDir)
	if err != nil {
		t.Fatalf("expected nil on double init, got: %v", err)
	}
}

func TestEnsureInitialized(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	os.MkdirAll(gitDir, 0755)

	// EnsureInitialized should auto-init when not initialized
	err := EnsureInitialized(context.Background(), gitDir)
	if err != nil {
		t.Fatalf("unexpected error from auto-init: %v", err)
	}
	if !IsInitialized(gitDir) {
		t.Fatal("should be initialized after EnsureInitialized auto-init")
	}

	// Calling again on an already-initialized repo should succeed
	err = EnsureInitialized(context.Background(), gitDir)
	if err != nil {
		t.Fatalf("unexpected error after init: %v", err)
	}
}

func TestUninstall(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	os.MkdirAll(gitDir, 0755)

	// Uninstall when not initialized
	err := Uninstall(context.Background(), gitDir)
	if err == nil {
		t.Fatal("expected error uninstalling when not initialized")
	}

	Init(context.Background(), gitDir)
	err = Uninstall(context.Background(), gitDir)
	if err != nil {
		t.Fatal(err)
	}

	if IsInitialized(gitDir) {
		t.Fatal("should not be initialized after uninstall")
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", cfg.SchemaVersion)
	}
	if cfg.Lock.AcquireTimeoutSeconds != 30 {
		t.Errorf("AcquireTimeoutSeconds = %d, want 30", cfg.Lock.AcquireTimeoutSeconds)
	}
	if cfg.Hooks.PrePrePush.TimeoutSeconds != 1800 {
		t.Errorf("PrePrePush.TimeoutSeconds = %d, want 1800", cfg.Hooks.PrePrePush.TimeoutSeconds)
	}
	if cfg.Push.RetryAttempts != 3 {
		t.Errorf("RetryAttempts = %d, want 3", cfg.Push.RetryAttempts)
	}
}

// TestRetiredLogKeyStillParses pins the removal of log.maxSizeMB: a
// config.json written by an older safegit keeps loading (unknown members are
// ignored), while setting or reading the key is a hard error.
func TestRetiredLogKeyStillParses(t *testing.T) {
	jsonData := `{
		"schemaVersion": 1,
		"commit": {"casMaxAttempts": 5},
		"lock": {"acquireTimeoutSeconds": 30},
		"hooks": {"preprepush": {"timeoutSeconds": 1800}},
		"push": {"retryAttempts": 3},
		"log": {"maxSizeMB": 100}
	}`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(jsonData), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfigFrom(path)
	if err != nil {
		t.Fatalf("a config carrying the retired log key must still load: %v", err)
	}
	if cfg.Push.RetryAttempts != 3 {
		t.Errorf("RetryAttempts = %d, want 3", cfg.Push.RetryAttempts)
	}

	if err := SetConfigValue(cfg, "log.maxSizeMB", "50"); err == nil {
		t.Error("setting log.maxSizeMB should be an unknown-key error")
	} else if !strings.Contains(err.Error(), "unknown config key") {
		t.Errorf("error should say unknown config key, got: %v", err)
	}
	if _, err := GetConfigValue(cfg, "log.maxSizeMB"); err == nil {
		t.Error("getting log.maxSizeMB should be an unknown-key error")
	}
	for _, k := range ValidConfigKeys() {
		if k == "log.maxSizeMB" {
			t.Error("log.maxSizeMB must not be a valid config key")
		}
	}
}

func TestAutoBumpParent_DefaultNil(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Commit.AutoBumpParent != nil {
		t.Errorf("AutoBumpParent should be nil by default, got %v", *cfg.Commit.AutoBumpParent)
	}
}

func TestAutoBumpParent_LoadWithoutField(t *testing.T) {
	// A config JSON without autoBumpParent should load with nil.
	jsonData := `{
		"schemaVersion": 1,
		"commit": {"casMaxAttempts": 5},
		"lock": {"acquireTimeoutSeconds": 30},
		"hooks": {"preprepush": {"timeoutSeconds": 1800}},
		"push": {"retryAttempts": 3},
		"log": {"maxSizeMB": 100}
	}`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, []byte(jsonData), 0644)

	cfg, err := LoadConfigFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Commit.AutoBumpParent != nil {
		t.Errorf("AutoBumpParent should be nil when absent from JSON, got %v", *cfg.Commit.AutoBumpParent)
	}
}

func TestAutoBumpParent_SetTrue(t *testing.T) {
	cfg := DefaultConfig()
	err := SetConfigValue(&cfg, "commit.autoBumpParent", "true")
	if err != nil {
		t.Fatal(err)
	}
	val, err := GetConfigValue(&cfg, "commit.autoBumpParent")
	if err != nil {
		t.Fatal(err)
	}
	boolPtr, ok := val.(*bool)
	if !ok {
		t.Fatalf("expected *bool, got %T", val)
	}
	if boolPtr == nil || !*boolPtr {
		t.Errorf("expected true, got %v", boolPtr)
	}
}

func TestAutoBumpParent_SetFalse(t *testing.T) {
	cfg := DefaultConfig()
	err := SetConfigValue(&cfg, "commit.autoBumpParent", "false")
	if err != nil {
		t.Fatal(err)
	}
	val, err := GetConfigValue(&cfg, "commit.autoBumpParent")
	if err != nil {
		t.Fatal(err)
	}
	boolPtr, ok := val.(*bool)
	if !ok {
		t.Fatalf("expected *bool, got %T", val)
	}
	if boolPtr == nil || *boolPtr {
		t.Errorf("expected false, got %v", boolPtr)
	}
}

func TestAutoBumpParent_SetInvalid(t *testing.T) {
	cfg := DefaultConfig()
	err := SetConfigValue(&cfg, "commit.autoBumpParent", "invalid")
	if err == nil {
		t.Fatal("expected error for invalid value")
	}
	err = SetConfigValue(&cfg, "commit.autoBumpParent", "1")
	if err == nil {
		t.Fatal("expected error for numeric value")
	}
	err = SetConfigValue(&cfg, "commit.autoBumpParent", "TRUE")
	if err == nil {
		t.Fatal("expected error for uppercase TRUE")
	}
}

func TestAutoBumpParent_SaveReload(t *testing.T) {
	cfg := DefaultConfig()
	v := true
	cfg.Commit.AutoBumpParent = &v

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	err := SaveConfigTo(path, &cfg)
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadConfigFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Commit.AutoBumpParent == nil {
		t.Fatal("AutoBumpParent should not be nil after reload")
	}
	if !*loaded.Commit.AutoBumpParent {
		t.Errorf("AutoBumpParent should be true after reload, got false")
	}

	// Also test with false
	f := false
	cfg.Commit.AutoBumpParent = &f
	err = SaveConfigTo(path, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadConfigFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Commit.AutoBumpParent == nil {
		t.Fatal("AutoBumpParent should not be nil after reload (false)")
	}
	if *loaded.Commit.AutoBumpParent {
		t.Errorf("AutoBumpParent should be false after reload, got true")
	}
}

func TestAutoBumpParent_NilOmittedFromJSON(t *testing.T) {
	cfg := DefaultConfig()
	// AutoBumpParent is nil by default
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	// The JSON should not contain "autoBumpParent"
	var raw map[string]json.RawMessage
	json.Unmarshal(data, &raw)
	var commitRaw map[string]json.RawMessage
	json.Unmarshal(raw["commit"], &commitRaw)
	if _, exists := commitRaw["autoBumpParent"]; exists {
		t.Errorf("autoBumpParent should be omitted from JSON when nil, but found in output: %s", string(data))
	}
}

func TestAutoBumpParent_GetValueNil(t *testing.T) {
	cfg := DefaultConfig()
	val, err := GetConfigValue(&cfg, "commit.autoBumpParent")
	if err != nil {
		t.Fatal(err)
	}
	boolPtr, ok := val.(*bool)
	if !ok {
		t.Fatalf("expected *bool, got %T", val)
	}
	if boolPtr != nil {
		t.Errorf("expected nil *bool for unset config, got %v", *boolPtr)
	}
}

// TestConcurrentFirstInitNeverPublishesPartialConfig pins the atomic
// config.json write. Init publishes the file that IsInitialized stats and every
// command then reads, so a plain write let a concurrent first init observe the
// empty prefix of a half-written file -- the observed flake was "unexpected end
// of JSON input" on a repo two sessions touched at once.
//
// 50 iterations of 8 concurrent first inits: each iteration races the writers
// against readers that must see either no file at all or a complete config,
// never anything in between. (The real statistical check is the stress run;
// this is the deterministic regression pin.)
func TestConcurrentFirstInitNeverPublishesPartialConfig(t *testing.T) {
	const iterations = 50
	const concurrency = 8

	for i := 0; i < iterations; i++ {
		gitDir := filepath.Join(t.TempDir(), ".git")
		if err := os.MkdirAll(gitDir, 0755); err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		errs := make(chan error, concurrency*2)

		for w := 0; w < concurrency; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := Init(context.Background(), gitDir); err != nil {
					errs <- fmt.Errorf("Init: %w", err)
				}
			}()
			// A reader racing the writers: the only two legal observations are
			// "not there yet" and "complete and parseable".
			wg.Add(1)
			go func() {
				defer wg.Done()
				for attempt := 0; attempt < 50; attempt++ {
					if !IsInitialized(gitDir) {
						continue
					}
					if _, err := LoadConfig(gitDir); err != nil {
						errs <- fmt.Errorf("a published config.json did not parse: %w", err)
						return
					}
				}
			}()
		}

		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("iteration %d: %v", i, err)
		}

		cfg, err := LoadConfig(gitDir)
		if err != nil {
			t.Fatalf("iteration %d: config after the race does not load: %v", i, err)
		}
		if cfg.SchemaVersion != 1 || cfg.Commit.CASMaxAttempts != 5 {
			t.Fatalf("iteration %d: config after the race is not the default: %+v", i, cfg)
		}

		// No temp files left behind by any of the losing writers.
		entries, err := os.ReadDir(SafegitDir(gitDir))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.Contains(e.Name(), ".tmp-") {
				t.Fatalf("iteration %d: leftover temp file %s", i, e.Name())
			}
		}
	}
}
