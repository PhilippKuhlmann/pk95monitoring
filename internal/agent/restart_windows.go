package agent

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows/svc/mgr"
)

// restartExitCode is the exit code after a self-update. The service is not reported as
// stopped, so the service control manager treats it as a failure and runs the recovery
// action (restart after 5 s, see ConfigureRecovery) with the new binary.
const restartExitCode = 3

// RestartSelf ends the process; the service manager starts the new binary.
func RestartSelf(string) error {
	os.Exit(restartExitCode)
	return nil
}

// ConfigureRecovery sets the recovery actions of the service: restart after 5 s, also when
// the process ends with an error code without crashing.
func ConfigureRecovery(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return err
	}
	defer s.Close()
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}
	if err := s.SetRecoveryActions(actions, uint32((24 * time.Hour).Seconds())); err != nil {
		return fmt.Errorf("Wiederherstellungsoptionen: %w", err)
	}
	return s.SetRecoveryActionsOnNonCrashFailures(true)
}

// replaceBinary swaps the running exe: Windows cannot overwrite a running image, but it can
// rename it. The old file stays as <exe>.old until the next start removes it.
func replaceBinary(newPath, exe string) error {
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return fmt.Errorf("laufendes Programm nicht umbenennbar: %w", err)
	}
	if err := os.Rename(newPath, exe); err != nil {
		_ = os.Rename(old, exe)
		return fmt.Errorf("neues Programm nicht einsetzbar: %w", err)
	}
	return nil
}

// removeOldBinary deletes the binary left behind by the last update.
func removeOldBinary(exe string) { _ = os.Remove(exe + ".old") }
