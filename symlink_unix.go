//go:build !windows

package main

func symlinkCommand(target, path string) []interface{} {
	return []interface{}{"ln", "-s", "--", target, path}
}
