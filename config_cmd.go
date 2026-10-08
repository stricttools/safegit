package main

import (
	"fmt"

	"github.com/stricttools/safegit/internal/exitcode"
	"github.com/stricttools/safegit/internal/repo"
	"github.com/stricttools/strictcli/go/strictcli"
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
	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		errorf(flags, "%v", err)
		return exitcode.NotInitialized
	}

	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		errorf(flags, "%v", err)
		return exitcode.General
	}

	for _, key := range repo.ValidConfigKeys() {
		val, _ := repo.GetConfigValue(cfg, key)
		outf(flags, "%s = %s", key, formatConfigValue(val))
	}
	return 0
}

func runConfigGet(flags globalFlags, key string) int {
	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		errorf(flags, "%v", err)
		return exitcode.NotInitialized
	}

	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		errorf(flags, "%v", err)
		return exitcode.General
	}

	val, err := repo.GetConfigValue(cfg, key)
	if err != nil {
		errorf(flags, "%v", err)
		return exitcode.General
	}
	outf(flags, "%s", formatConfigValue(val))
	return 0
}

func runConfigSet(flags globalFlags, key, value string) int {
	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		errorf(flags, "%v", err)
		return exitcode.NotInitialized
	}

	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		errorf(flags, "%v", err)
		return exitcode.General
	}

	if err := repo.SetConfigValue(cfg, key, value); err != nil {
		errorf(flags, "%v", err)
		return exitcode.General
	}

	data, err := repo.MarshalConfig(cfg)
	if err != nil {
		errorf(flags, "%v", err)
		return exitcode.General
	}
	configPath := flags.configPath
	if configPath == "" {
		configPath = repo.ConfigPath(gitDir)
	}
	// Minting the write on the handle is what makes `config set --dry-run`
	// record the change instead of performing it.
	if _, err := flags.Effects().Write(configPath, data, strictcli.Resource("safegit-config:"+configPath)); err != nil {
		errorf(flags, "%v", err)
		return exitcode.General
	}
	if !flags.dryRun {
		infof(flags, "%s = %s", key, value)
	}
	return 0
}
