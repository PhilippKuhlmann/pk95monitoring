//go:build !windows

package main

import (
	"fmt"
	"os"
)

const adminHint = "Bitte als root ausführen (sudo pk95agent …)"

func isAdmin() bool { return os.Geteuid() == 0 }

func installPath() string { return "/usr/local/bin/pk95agent" }

// removeInstalledBinary deletes the installed program (a running process keeps its inode).
func removeInstalledBinary() {
	if err := os.Remove(installPath()); err == nil {
		fmt.Printf("Programm entfernt: %s\n", installPath())
	}
}
