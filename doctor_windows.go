package main

import (
	"fmt"
	"golang.org/x/sys/windows"
	"path/filepath"
)

func checkFilesystem(gitDir string) checkResult {
	root := filepath.VolumeName(gitDir) + `\`
	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return checkResult{Name: "filesystem", Status: "warn", Detail: err.Error()}
	}
	if windows.GetDriveType(p) == windows.DRIVE_REMOTE {
		return checkResult{Name: "filesystem", Status: "warn", Detail: "network filesystem (lock atomicity not guaranteed)"}
	}
	var flags uint32
	if err := windows.GetVolumeInformation(p, nil, 0, nil, nil, &flags, nil, 0); err != nil {
		return checkResult{Name: "filesystem", Status: "warn", Detail: fmt.Sprintf("volume query failed: %v", err)}
	}
	if flags&windows.FILE_SUPPORTS_HARD_LINKS == 0 {
		return checkResult{Name: "filesystem", Status: "warn", Detail: "filesystem lacks hard links required for atomic lock publication"}
	}
	return checkResult{Name: "filesystem", Status: "ok"}
}
