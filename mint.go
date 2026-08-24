package main

import (
	"os"

	"github.com/smm-h/strictcli/go/strictcli"
)

// mintedRemover returns the file removal safegit's cleanup paths perform: the
// same signature os.Remove has, minted through the effects handle so that a
// preview RECORDS the removal instead of making it and machine mode carries it.
//
// It stats first, and that is not belt and braces. strictcli's Effects.Remove is
// os.RemoveAll -- a missing path is a success -- while both callers read a nil
// error as "the file I judged was there is now gone": lock reclamation would
// turn a path that vanished under it into a reclamation it claims to have
// performed, and `unlock` would report releasing a lock that was not there. The
// stat is what keeps the absence an error, the way os.Remove reports it.
//
// resource is the effect record's resource token prefix; the path is appended to
// it, so two removals of different paths never collapse into one resource.
func mintedRemover(flags globalFlags, resource string) func(string) error {
	return func(path string) error {
		if _, err := os.Lstat(path); err != nil {
			return err
		}
		if _, err := flags.effects().Remove(path, strictcli.Resource(resource+path)); err != nil {
			return err
		}
		return nil
	}
}
