package lock

import "os"

// Go translates console Ctrl-C and Ctrl-Break to os.Interrupt on Windows.
func cleanupSignals() []os.Signal { return []os.Signal{os.Interrupt} }
func signalExitStatus(sig os.Signal) int {
	if sig == os.Interrupt {
		return 130
	}
	return 1
}
