//go:build !windows

package agent

import (
	"os"
	"syscall"
)

// RestartSelf replaces the running process with exe (same arguments and PID), so systemd
// keeps tracking the service and nothing has to restart it.
func RestartSelf(exe string) error {
	return syscall.Exec(exe, os.Args, os.Environ())
}

// ConfigureRecovery is a no-op: systemd restarts the unit (Restart=always).
func ConfigureRecovery(string) error { return nil }

// replaceBinary moves the verified new binary over the running one. Renaming within the same
// directory is atomic; the running process keeps its (now unlinked) inode.
func replaceBinary(newPath, exe string) error {
	if err := os.Chmod(newPath, 0o755); err != nil {
		return err
	}
	return os.Rename(newPath, exe)
}

// removeOldBinary is a no-op: nothing is left behind on Unix.
func removeOldBinary(string) {}
