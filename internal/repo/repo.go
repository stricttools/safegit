// Package repo manages the .git/safegit/ data directory including initialization, configuration loading, validation, and path helpers for all state files.
package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/hooks"
)

// Config holds safegit configuration persisted in config.json.
//
// A key this struct no longer declares (log.maxSizeMB, removed with oplog
// rotation) still LOADS from an existing config.json: encoding/json ignores
// unknown members. Writing one does not: GetConfigValue and SetConfigValue
// answer "unknown config key" for anything outside ValidConfigKeys.
type Config struct {
	SchemaVersion int          `json:"schemaVersion"`
	Commit        CommitConfig `json:"commit"`
	Lock          LockConfig   `json:"lock"`
	Hooks         HooksConfig  `json:"hooks"`
	Push          PushConfig   `json:"push"`
}

// CommitConfig holds commit-related settings.
type CommitConfig struct {
	CASMaxAttempts int   `json:"casMaxAttempts"`
	AutoBumpParent *bool `json:"autoBumpParent,omitempty"`
}

// LockConfig holds ref-lock acquisition settings.
type LockConfig struct {
	AcquireTimeoutSeconds int `json:"acquireTimeoutSeconds"`
}

// HooksConfig holds hook-related settings.
type HooksConfig struct {
	PrePrePush PrePrePushConfig `json:"preprepush"`
}

// PrePrePushConfig holds pre-pre-push hook timeout settings.
type PrePrePushConfig struct {
	TimeoutSeconds int `json:"timeoutSeconds"`
}

// PushConfig holds push retry settings.
type PushConfig struct {
	RetryAttempts int `json:"retryAttempts"`
}

// DefaultConfig returns the default safegit configuration.
func DefaultConfig() Config {
	return Config{
		SchemaVersion: 1,
		Commit:        CommitConfig{CASMaxAttempts: 5},
		Lock:          LockConfig{AcquireTimeoutSeconds: 30},
		Hooks:         HooksConfig{PrePrePush: PrePrePushConfig{TimeoutSeconds: 1800}},
		Push:          PushConfig{RetryAttempts: 3},
	}
}

// SafegitDir returns the path to .git/safegit/ given a .git directory path.
func SafegitDir(gitDir string) string {
	return filepath.Join(gitDir, "safegit")
}

// SharedGitDir returns the COMMON git directory: the one every worktree of a
// repository shares. For a normal repository it is gitDir itself; for a linked
// worktree, whose git dir is <common>/worktrees/<name>, it is <common>.
//
// It is the anchor for everything that is repository-level policy rather than
// checkout state -- the ref locks, and safegit's live hook store -- so that two
// worktrees can never disagree about it. Git's own hook directory is common
// too, which is why the pre-migration hook location is resolved from here.
//
// The parameter accepts either the git directory (.git) or the safegit
// directory (.git/safegit); callers use both forms.
//
// The answer never depends on the process working directory. CommonGitDirOf
// runs git in the git directory it is handed, so an answer that comes back
// relative is relative to THAT directory and is anchored there -- filepath.Abs,
// which would resolve it against this process's own directory, is exactly the
// wrong anchor and is not used.
func SharedGitDir(ctx context.Context, gitDir string) string {
	// Normalize: some callers pass the safegit dir instead of the git dir.
	actualGitDir := gitDir
	if filepath.Base(gitDir) == "safegit" {
		actualGitDir = filepath.Dir(gitDir)
	}
	commonDir, err := git.CommonGitDirOf(ctx, actualGitDir)
	if err != nil || commonDir == "" {
		return actualGitDir
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(actualGitDir, commonDir)
	}
	return filepath.Clean(commonDir)
}

// SharedSafegitDir returns the safegit directory under the common .git dir.
// For normal repos this is identical to SafegitDir(gitDir). For worktrees it
// returns <common-git-dir>/safegit so that lock files and the live hook store
// are shared across all worktrees, ensuring proper serialization of ref updates
// and one repository-wide answer to which hooks run.
//
// It takes the same parameter forms as SharedGitDir, whose resolution it is.
func SharedSafegitDir(ctx context.Context, gitDir string) string {
	return filepath.Join(SharedGitDir(ctx, gitDir), "safegit")
}

// IsInitialized reports whether this repository has a usable safegit data
// directory, which is decided by config.json rather than by the directory
// alone. A directory that exists without config.json is half-initialized -- an
// interrupted Init, or any stray subdirectory created under it -- and reporting
// that as initialized would make EnsureInitialized a no-op and leave every
// command failing on the missing config.json. Reporting it as uninitialized
// lets Init complete it (Init is idempotent over the directories it creates).
func IsInitialized(gitDir string) bool {
	_, err := os.Stat(filepath.Join(SafegitDir(gitDir), "config.json"))
	return err == nil
}

// Init creates the .git/safegit/ directory structure and writes default config.json.
// Idempotent: returns nil if already initialized.
//
// The context is the dispatch's own: the worktree check below asks git where
// the common git directory is, and that call belongs on the same context as
// every other git call the invocation makes.
func Init(ctx context.Context, gitDir string) error {
	if IsInitialized(gitDir) {
		return nil
	}

	sgDir := SafegitDir(gitDir)

	// Create directory structure (MkdirAll creates all parents, so sgDir
	// itself does not need a separate call)
	dirs := []string{
		filepath.Join(sgDir, "locks", "refs", "heads"),
		filepath.Join(sgDir, "tmp"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("creating directory %s: %w", d, err)
		}
	}

	// If running inside a worktree, also create the shared locks dir under
	// the common .git dir so that ref locks are visible to all worktrees.
	sharedDir := SharedSafegitDir(ctx, gitDir)
	if sharedDir != sgDir {
		if err := os.MkdirAll(filepath.Join(sharedDir, "locks", "refs", "heads"), 0755); err != nil {
			return fmt.Errorf("creating shared locks directory %s: %w", sharedDir, err)
		}
	}

	// Write config.json through a temp file and a rename, so it appears
	// complete or not at all.
	//
	// IsInitialized stats this exact path and every command then READS it, so a
	// plain write publishes a path whose content is still being written: a
	// concurrent first init in the same repo used to observe the empty prefix
	// and fail with "unexpected end of JSON input". The temp name comes from
	// os.CreateTemp in the SAME directory (rename is only atomic within a
	// filesystem) and is per-process unique -- a fixed temp name would just
	// move the race onto the temp file.
	cfg := DefaultConfig()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	configPath := filepath.Join(sgDir, "config.json")
	if err := writeFileAtomic(configPath, append(data, '\n'), 0644); err != nil {
		return fmt.Errorf("writing config.json: %w", err)
	}

	// Create empty log file
	logPath := filepath.Join(sgDir, "log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("creating log file: %w", err)
	}
	f.Close()

	return nil
}

// writeFileAtomic writes data to path via a temp file in the same directory,
// fsynced and then renamed, so a reader ever sees the old content or the new
// one, never a partial write, and a crash cannot publish an empty file. A
// failed write leaves no temp file behind.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmpPath := f.Name()
	cleanup := func() {
		f.Close()
		os.Remove(tmpPath)
	}
	if _, err := f.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("writing %s: %w", tmpPath, err)
	}
	// CreateTemp makes the file 0600; the published file needs the caller's
	// mode, and chmod before the rename so it is never briefly readable-wrong.
	// Unlike os.WriteFile's create mode, an explicit chmod is not filtered by
	// the process umask, so the file lands at exactly perm.
	if err := f.Chmod(perm); err != nil {
		cleanup()
		return fmt.Errorf("setting mode on %s: %w", tmpPath, err)
	}
	// The rename handles the race (a reader sees old content or new, never a
	// partial write); the fsync handles the crash. Without it the rename can
	// reach the disk before the data does, and a crash in that window publishes
	// config.json at zero length -- which reads back as "unexpected end of JSON
	// input" forever, not as a missing file that would be re-initialized.
	if err := f.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("syncing %s: %w", tmpPath, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("closing %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("renaming %s to %s: %w", tmpPath, path, err)
	}
	return nil
}

// EnsureInitialized auto-initializes .git/safegit/ if it doesn't exist yet.
func EnsureInitialized(ctx context.Context, gitDir string) error {
	if !IsInitialized(gitDir) {
		if err := Init(ctx, gitDir); err != nil {
			return fmt.Errorf("safegit: auto-init failed: %w", err)
		}
		fmt.Fprintf(os.Stderr, "safegit: auto-initialized %s\n", SafegitDir(gitDir))
	}
	return nil
}

// Uninstall removes safegit from a repository: the .git/safegit/ directory
// entirely -- which since the hook store moved there takes the installed hooks
// with it -- plus the repository-level state under the common git dir in
// worktree setups (the shared locks and the live hook store, which is the one
// pushes actually run), plus the two safegit-owned names that may still be
// sitting in git's own hook directory from before the move. Leaving any of
// those behind would keep an uninstalled tool's checks running on every push
// with no .git/safegit/ left to explain where they came from.
//
// The hook store the CHECKOUT provides (.safegit/hooks in the work tree) is
// deliberately untouched: it is part of the repository's content, shared with
// everyone who cloned it, and removing it here would be an uncommitted deletion
// of somebody else's file.
func Uninstall(ctx context.Context, gitDir string) error {
	sgDir := SafegitDir(gitDir)
	if _, err := os.Stat(sgDir); os.IsNotExist(err) {
		return errors.New("safegit is not initialized (nothing to remove)")
	}
	if err := os.RemoveAll(sgDir); err != nil {
		return err
	}
	shared := SharedGitDir(ctx, gitDir)
	if sharedDir := filepath.Join(shared, "safegit"); sharedDir != sgDir {
		for _, name := range []string{"locks", "hooks"} {
			os.RemoveAll(filepath.Join(sharedDir, name))
		}
	}
	for _, legacy := range []string{hooks.LegacyFile(shared), hooks.LegacyDir(shared)} {
		if err := os.RemoveAll(legacy); err != nil {
			return err
		}
	}
	return nil
}

// LoadConfig reads and parses config.json from the safegit directory.
func LoadConfig(gitDir string) (*Config, error) {
	configPath := filepath.Join(SafegitDir(gitDir), "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("reading config.json: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config.json: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config.json: %w", err)
	}
	return &cfg, nil
}

// LoadConfigFrom reads and parses config from an arbitrary path.
func LoadConfigFrom(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return &cfg, nil
}

// Validate checks that all config values are within acceptable ranges.
func (c *Config) Validate() error {
	checks := []struct {
		name string
		val  int
	}{
		{"commit.casMaxAttempts", c.Commit.CASMaxAttempts},
		{"lock.acquireTimeoutSeconds", c.Lock.AcquireTimeoutSeconds},
		{"hooks.preprepush.timeoutSeconds", c.Hooks.PrePrePush.TimeoutSeconds},
		{"push.retryAttempts", c.Push.RetryAttempts},
	}
	for _, ch := range checks {
		if ch.val <= 0 {
			return fmt.Errorf("%s must be positive (got %d)", ch.name, ch.val)
		}
	}
	return nil
}

// MarshalConfig renders config.json's exact bytes. Rendering is split from
// writing so callers mint the write as an effect instead of performing it here,
// which is what lets --dry-run record a config change without making one.
//
// This package therefore writes config.json in exactly one place -- Init, whose
// write goes through writeFileAtomic. `config set` renders here and hands the
// bytes to the effects handle. A save helper that plain-writes the file would
// be a third, non-atomic writer of the path every command reads.
func MarshalConfig(cfg *Config) ([]byte, error) {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling config: %w", err)
	}
	return append(data, '\n'), nil
}

// ConfigPath is where config.json lives for the given git dir.
func ConfigPath(gitDir string) string {
	return filepath.Join(SafegitDir(gitDir), "config.json")
}

// GetConfigValue returns the value for a dot-separated config key.
func GetConfigValue(cfg *Config, key string) (interface{}, error) {
	switch key {
	case "commit.casMaxAttempts":
		return cfg.Commit.CASMaxAttempts, nil
	case "commit.autoBumpParent":
		return cfg.Commit.AutoBumpParent, nil
	case "lock.acquireTimeoutSeconds":
		return cfg.Lock.AcquireTimeoutSeconds, nil
	case "hooks.preprepush.timeoutSeconds":
		return cfg.Hooks.PrePrePush.TimeoutSeconds, nil
	case "push.retryAttempts":
		return cfg.Push.RetryAttempts, nil
	default:
		return nil, fmt.Errorf("unknown config key: %s", key)
	}
}

// SetConfigValue sets a dot-separated config key to the given string value.
//
// The key is resolved BEFORE the value is parsed, so an unknown key is always
// reported as an unknown key whatever its value looks like: `config set
// log.maxSizeMB abc` names the retired key, not the shape of "abc".
func SetConfigValue(cfg *Config, key, value string) error {
	// Handle boolean config keys separately.
	switch key {
	case "commit.autoBumpParent":
		switch value {
		case "true":
			v := true
			cfg.Commit.AutoBumpParent = &v
		case "false":
			v := false
			cfg.Commit.AutoBumpParent = &v
		default:
			return fmt.Errorf("invalid value %q for %s: must be \"true\" or \"false\"", value, key)
		}
		return nil
	}

	// Resolving the key to the field it names is the key check: an unknown key
	// leaves here, and only a known integer key reaches the value parse below.
	var target *int
	switch key {
	case "commit.casMaxAttempts":
		target = &cfg.Commit.CASMaxAttempts
	case "lock.acquireTimeoutSeconds":
		target = &cfg.Lock.AcquireTimeoutSeconds
	case "hooks.preprepush.timeoutSeconds":
		target = &cfg.Hooks.PrePrePush.TimeoutSeconds
	case "push.retryAttempts":
		target = &cfg.Push.RetryAttempts
	default:
		return fmt.Errorf("unknown config key: %s", key)
	}

	// strconv.Atoi parses the WHOLE string or fails. A scan
	// (fmt.Sscanf("%d")) stops at the first non-digit and reports success on
	// the prefix it consumed, so `config set push.retryAttempts 5abc` used to
	// store 5 -- a typo silently accepted as a value the operator never typed.
	intVal, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("invalid value %q for %s: must be an integer", value, key)
	}
	if intVal <= 0 {
		return fmt.Errorf("invalid value %q for %s: must be positive", value, key)
	}
	*target = intVal
	return nil
}

// ValidConfigKeys returns the list of supported config keys.
func ValidConfigKeys() []string {
	return []string{
		"commit.casMaxAttempts",
		"commit.autoBumpParent",
		"lock.acquireTimeoutSeconds",
		"hooks.preprepush.timeoutSeconds",
		"push.retryAttempts",
	}
}
