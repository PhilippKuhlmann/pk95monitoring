package main

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

const adminHint = "Bitte in einer Eingabeaufforderung oder PowerShell „Als Administrator ausführen“"

func isAdmin() bool { return windows.GetCurrentProcessToken().IsElevated() }

func installPath() string {
	base := os.Getenv("ProgramFiles")
	if base == "" {
		base = `C:\Program Files`
	}
	return filepath.Join(base, "PK95Agent", "pk95agent.exe")
}

// removeInstalledBinary: a running .exe cannot be deleted on Windows, so it is only named.
func removeInstalledBinary() {
	fmt.Printf("Das Programmverzeichnis kann gelöscht werden: %s\n", filepath.Dir(installPath()))
}
