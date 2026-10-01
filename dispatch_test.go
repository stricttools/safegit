package main

import (
	"testing"

	"github.com/stricttools/strictcli/go/strictcli"
)

// dispatch runs fn inside a dispatch of a throwaway app, so the globalFlags fn
// is handed carry a real framework Context -- the only route safegit's writers
// have to the operator. reserved holds the framework flags to set (for example
// "--json" or "--quiet"); the returned Result holds what the run wrote.
func dispatch(t *testing.T, reserved []string, fn func(flags globalFlags)) strictcli.Result {
	t.Helper()
	app := strictcli.NewApp("probe", "0", "a throwaway app that gives a test a dispatch context")
	app.Command("probe", "run the function under test", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		fn(globalsToFlags(ctx, kwargs))
		return strictcli.Exit(0)
	}, strictcli.WithEffect(strictcli.EffectMutating))
	return app.Test(append(append([]string{}, reserved...), "probe"))
}
