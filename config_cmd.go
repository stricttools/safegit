package main

import (
	"fmt"
	"os"

	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/strictcli/go/strictcli"
)

// formatConfigValue formats a config value for display.
// Handles *bool (prints "true", "false", or "not set") and other types via %v.
func formatConfigValue(val interface{}) string {
	if val == nil {
		return "not set"
	}
	switch v := val.(type) {
	case *bool:
		if v == nil {
			return "not set"
		}
		if *v {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", val)
	}
}

func runConfigShow(flags globalFlags) int {
	gitDir := mustGitDir(flags, "config")
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 4
	}

	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	for _, key := range repo.ValidConfigKeys() {
		val, _ := repo.GetConfigValue(cfg, key)
		outf(flags, "%s = %s\n", key, formatConfigValue(val))
	}
	return 0
}

func runConfigGet(flags globalFlags, key string) int {
	gitDir := mustGitDir(flags, "config")
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 4
	}

	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	val, err := repo.GetConfigValue(cfg, key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	outf(flags, "%s\n", formatConfigValue(val))
	return 0
}

func runConfigSet(flags globalFlags, key, value string) int {
	gitDir := mustGitDir(flags, "config")
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 4
	}

	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if err := repo.SetConfigValue(cfg, key, value); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	data, err := repo.MarshalConfig(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	configPath := flags.configPath
	if configPath == "" {
		configPath = repo.ConfigPath(gitDir)
	}
	// Minting the write on the handle is what makes `config set --dry-run`
	// record the change instead of performing it.
	if _, err := flags.effects().Write(configPath, data, strictcli.Resource("safegit-config:"+configPath)); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if !flags.silent() && !flags.dryRun {
		fmt.Printf("%s = %s\n", key, value)
	}
	return 0
}
