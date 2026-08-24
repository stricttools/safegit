package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The integer config keys are POSITIVE by construction, and this file is where
// that construction is pinned.
//
// It exists because the callers used to re-decide it. Every reader of these
// four keys carried its own `if value <= 0 { value = <the default> }` arm, so
// the real answer to "what happens when the config says 0" was written down in
// five places at once -- the loader's refusal and one silent substitution per
// call site -- and only the loader's was ever reached. Those arms are deleted;
// what makes deleting them safe is that a non-positive value never survives
// long enough to be read, and these pins are that fact, held down per key.
//
// Two writers exist, and both are covered:
//
//   - the LOADER (LoadConfig / LoadConfigFrom), pinned here for a value written
//     as zero, written as negative, and omitted from the file altogether (an
//     absent JSON member unmarshals to 0, so absence and zero are the same
//     refusal);
//   - SetConfigValue, pinned here for the same non-positive spellings, so a
//     `safegit config set` can never put a value in the file that the loader
//     would then refuse.
//
// DefaultConfig is pinned too: it is the third source of a Config, used when a
// dry run runs before auto-init has written config.json, and its values must
// satisfy the same rule with no loader in the path to check them.

// configKeys names each integer key by its config-set spelling and by the
// accessor that reads it, so a key added later without a positive-value rule
// fails to compile here rather than passing silently.
var configKeys = []struct {
	name string
	// jsonWith renders a whole config.json in which this key holds the given
	// literal and every other key holds a valid value.
	jsonWith func(literal string) string
	// omitted renders a whole config.json from which this key is absent.
	omitted string
	read    func(*Config) int
}{
	{
		name: "commit.casMaxAttempts",
		jsonWith: func(literal string) string {
			return configJSON(`"commit": {"casMaxAttempts": `+literal+`}`,
				`"lock": {"acquireTimeoutSeconds": 30}`,
				`"hooks": {"preprepush": {"timeoutSeconds": 1800}}`,
				`"push": {"retryAttempts": 3}`)
		},
		omitted: configJSON(`"lock": {"acquireTimeoutSeconds": 30}`,
			`"hooks": {"preprepush": {"timeoutSeconds": 1800}}`,
			`"push": {"retryAttempts": 3}`),
		read: func(c *Config) int { return c.Commit.CASMaxAttempts },
	},
	{
		name: "lock.acquireTimeoutSeconds",
		jsonWith: func(literal string) string {
			return configJSON(`"commit": {"casMaxAttempts": 5}`,
				`"lock": {"acquireTimeoutSeconds": `+literal+`}`,
				`"hooks": {"preprepush": {"timeoutSeconds": 1800}}`,
				`"push": {"retryAttempts": 3}`)
		},
		omitted: configJSON(`"commit": {"casMaxAttempts": 5}`,
			`"hooks": {"preprepush": {"timeoutSeconds": 1800}}`,
			`"push": {"retryAttempts": 3}`),
		read: func(c *Config) int { return c.Lock.AcquireTimeoutSeconds },
	},
	{
		name: "hooks.preprepush.timeoutSeconds",
		jsonWith: func(literal string) string {
			return configJSON(`"commit": {"casMaxAttempts": 5}`,
				`"lock": {"acquireTimeoutSeconds": 30}`,
				`"hooks": {"preprepush": {"timeoutSeconds": `+literal+`}}`,
				`"push": {"retryAttempts": 3}`)
		},
		omitted: configJSON(`"commit": {"casMaxAttempts": 5}`,
			`"lock": {"acquireTimeoutSeconds": 30}`,
			`"push": {"retryAttempts": 3}`),
		read: func(c *Config) int { return c.Hooks.PrePrePush.TimeoutSeconds },
	},
	{
		name: "push.retryAttempts",
		jsonWith: func(literal string) string {
			return configJSON(`"commit": {"casMaxAttempts": 5}`,
				`"lock": {"acquireTimeoutSeconds": 30}`,
				`"hooks": {"preprepush": {"timeoutSeconds": 1800}}`,
				`"push": {"retryAttempts": `+literal+`}`)
		},
		omitted: configJSON(`"commit": {"casMaxAttempts": 5}`,
			`"lock": {"acquireTimeoutSeconds": 30}`,
			`"hooks": {"preprepush": {"timeoutSeconds": 1800}}`),
		read: func(c *Config) int { return c.Push.RetryAttempts },
	},
}

// configJSON assembles a config.json body from whole member spellings.
func configJSON(members ...string) string {
	return "{\n\t\"schemaVersion\": 1,\n\t" + strings.Join(members, ",\n\t") + "\n}\n"
}

// writeConfigFile drops a config.json in a fresh temp dir and returns its path.
func writeConfigFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoaderRefusesNonPositiveConfigValues: no reader of these keys ever sees a
// non-positive value, because the loader refuses the file that carries one.
func TestLoaderRefusesNonPositiveConfigValues(t *testing.T) {
	for _, key := range configKeys {
		t.Run(key.name, func(t *testing.T) {
			for _, literal := range []string{"0", "-1", "-2147483648"} {
				path := writeConfigFile(t, key.jsonWith(literal))
				cfg, err := LoadConfigFrom(path)
				if err == nil {
					t.Fatalf("%s = %s loaded (%+v); a non-positive value must be refused, "+
						"because every reader takes the value as it stands", key.name, literal, cfg)
				}
				if !strings.Contains(err.Error(), key.name) {
					t.Errorf("the refusal of %s = %s does not name the key: %v", key.name, literal, err)
				}
			}

			// Absence is the same refusal: an omitted JSON member unmarshals to
			// zero, and a reader cannot tell that apart from a written zero.
			path := writeConfigFile(t, key.omitted)
			if cfg, err := LoadConfigFrom(path); err == nil {
				t.Fatalf("a config with %s omitted loaded (%+v); absence unmarshals to zero "+
					"and must be refused like one", key.name, cfg)
			} else if !strings.Contains(err.Error(), key.name) {
				t.Errorf("the refusal of an omitted %s does not name the key: %v", key.name, err)
			}

			// The positive spelling still loads and arrives unchanged -- the
			// refusal is about non-positive values, not about the key.
			path = writeConfigFile(t, key.jsonWith("7"))
			cfg, err := LoadConfigFrom(path)
			if err != nil {
				t.Fatalf("%s = 7 must load: %v", key.name, err)
			}
			if got := key.read(cfg); got != 7 {
				t.Errorf("%s loaded as %d, want 7", key.name, got)
			}
		})
	}
}

// TestLoadConfigRefusesNonPositiveConfigValues pins the same rule on the
// repository-directory loader, which is the one every command actually calls.
func TestLoadConfigRefusesNonPositiveConfigValues(t *testing.T) {
	for _, key := range configKeys {
		t.Run(key.name, func(t *testing.T) {
			gitDir := filepath.Join(t.TempDir(), ".git")
			if err := os.MkdirAll(SafegitDir(gitDir), 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(SafegitDir(gitDir), "config.json")
			if err := os.WriteFile(path, []byte(key.jsonWith("0")), 0o644); err != nil {
				t.Fatal(err)
			}
			if cfg, err := LoadConfig(gitDir); err == nil {
				t.Fatalf("%s = 0 loaded through LoadConfig (%+v); it must be refused", key.name, cfg)
			} else if !strings.Contains(err.Error(), key.name) {
				t.Errorf("the refusal does not name %s: %v", key.name, err)
			}
		})
	}
}

// TestSetConfigValueRefusesNonPositiveValues: the writer cannot put a value in
// the file that the loader would then refuse.
func TestSetConfigValueRefusesNonPositiveValues(t *testing.T) {
	for _, key := range configKeys {
		t.Run(key.name, func(t *testing.T) {
			for _, literal := range []string{"0", "-1"} {
				cfg := DefaultConfig()
				before := key.read(&cfg)
				if err := SetConfigValue(&cfg, key.name, literal); err == nil {
					t.Errorf("config set %s %s was accepted; it must be refused", key.name, literal)
				}
				if got := key.read(&cfg); got != before {
					t.Errorf("a refused %s write still changed the value: %d -> %d", key.name, before, got)
				}
			}
		})
	}
}

// TestDefaultConfigValuesArePositive: DefaultConfig is the third source of a
// Config -- a dry run before auto-init reads it with no loader in the path --
// so its values satisfy the rule on their own.
func TestDefaultConfigValuesArePositive(t *testing.T) {
	cfg := DefaultConfig()
	for _, key := range configKeys {
		if got := key.read(&cfg); got <= 0 {
			t.Errorf("DefaultConfig().%s = %d, want a positive value", key.name, got)
		}
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("DefaultConfig() does not pass Validate: %v", err)
	}
}

// TestConfigKeysCoverEveryValidatedKey: the table above is the enumeration this
// file's pins iterate, so a key added to Validate without a pin here fails.
func TestConfigKeysCoverEveryValidatedKey(t *testing.T) {
	pinned := map[string]bool{}
	for _, key := range configKeys {
		pinned[key.name] = true
	}
	// Validate reports one key at a time, so the zero Config is walked key by
	// key: each refusal names a key, which is then given a valid value so the
	// next refusal names the next one.
	var cfg Config
	seen := map[string]bool{}
	for {
		err := cfg.Validate()
		if err == nil {
			break
		}
		name := strings.SplitN(err.Error(), " ", 2)[0]
		if seen[name] {
			t.Fatalf("Validate keeps reporting %s after it was satisfied: %v", name, err)
		}
		seen[name] = true
		if !pinned[name] {
			t.Errorf("Validate checks %s, which has no positive-value pin in this file", name)
		}
		if err := SetConfigValue(&cfg, name, "1"); err != nil {
			t.Fatalf("cannot satisfy the key Validate reported (%s): %v", name, err)
		}
	}
	for name := range pinned {
		if !seen[name] {
			t.Errorf("this file pins %s, but Validate does not check it", name)
		}
	}
}
