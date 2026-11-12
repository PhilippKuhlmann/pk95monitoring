package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"pk95monitoring/internal/model"
)

// updatesWait is how long a report waits for a running update search.
const updatesWait = 20 * time.Second

// platformState holds the Linux caches.
type platformState struct {
	updates *periodic[updateCount]
}

func platformMachineID() string {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) >= 16 {
			return string(b)
		}
	}
	return ""
}

func (c *Collector) platformChecks(ctx context.Context, cfg Config, _ HostInfo) []model.Check {
	var out []model.Check
	if systemdBooted() {
		out = append(out, systemdCheck(ctx))
		out = append(out, serviceChecks(ctx, cfg.Services)...)
	}
	if c.plat.updates == nil {
		c.plat.updates = newPeriodic(6*time.Hour, 30*time.Minute, 5*time.Minute, linuxUpdates)
	}
	if ch, ok := updatesCheck(ctx, c.plat.updates, c.WaitSlow); ok {
		out = append(out, ch)
	}
	return out
}

// systemdBooted reports whether systemd is the running init system (sd_booted): containers
// and sysvinit hosts may have systemctl installed without systemd running.
func systemdBooted() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	st, err := os.Stat("/run/systemd/system")
	return err == nil && st.IsDir()
}

// run executes a command with a timeout and returns its standard output. A failing command's
// error carries the start of its standard error (an *exec.ExitError stays reachable via As).
func run(ctx context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	out, err := cmd.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(bytes.TrimSpace(ee.Stderr)) > 0 {
		err = fmt.Errorf("%w: %s", err, truncate(string(ee.Stderr), 200))
	}
	return string(out), err
}

func systemdCheck(ctx context.Context) model.Check {
	ch := model.Check{Key: "systemd", Name: "Systemd-Units", Group: groupServices}
	out, err := run(ctx, 30*time.Second, "systemctl", "list-units", "--state=failed", "--no-legend", "--plain", "--no-pager")
	if err != nil {
		ch.State, ch.Message = model.StateUnknown, "systemctl: "+err.Error()
		return ch
	}
	failed := parseFailedUnits(out)
	ch.Metrics = []model.Metric{{Name: "services.failed", Value: float64(len(failed))}}
	if len(failed) == 0 {
		ch.State, ch.Message = model.StateOK, "Keine fehlgeschlagenen Units"
		return ch
	}
	ch.State = model.StateWarn
	ch.Message = fmt.Sprintf("%d fehlgeschlagen: %s", len(failed), truncate(strings.Join(failed, ", "), 200))
	return ch
}

// serviceChecks reports every configured unit; it must be active.
func serviceChecks(ctx context.Context, services []string) []model.Check {
	var units []string
	for _, s := range services {
		if s = strings.TrimSpace(s); s != "" {
			units = append(units, s)
		}
	}
	if len(units) == 0 {
		return nil
	}
	// is-active exits non-zero when a unit is not active; the output is still complete.
	out, err := run(ctx, 30*time.Second, "systemctl", append([]string{"is-active", "--"}, units...)...)
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		out = ""
	}
	states := parseIsActive(units, out)
	checks := make([]model.Check, 0, len(units))
	for _, u := range units {
		st := states[u]
		ch := model.Check{Key: "svc:" + u, Name: "Dienst " + u, Group: groupServices, Message: unitStateText(st)}
		switch st {
		case "active":
			ch.State = model.StateOK
		case "activating", "reloading":
			ch.State = model.StateWarn
		default:
			ch.State = model.StateCrit
		}
		checks = append(checks, ch)
	}
	return checks
}

// linuxUpdates counts pending updates with apt (Debian, Ubuntu, Proxmox) or dnf.
func linuxUpdates(ctx context.Context) (updateCount, error) {
	if _, err := exec.LookPath("apt-get"); err == nil {
		out, err := run(ctx, 4*time.Minute, "apt-get", "-s", "-o", "Debug::NoLocking=1", "upgrade")
		if err != nil {
			return updateCount{}, fmt.Errorf("apt-get: %w", err)
		}
		return parseAptUpgrade(out), nil
	}
	if _, err := exec.LookPath("dnf"); err == nil {
		out, err := run(ctx, 4*time.Minute, "dnf", "-q", "check-update")
		var ee *exec.ExitError
		switch {
		case err == nil: // exit 0: nothing to do
			return updateCount{}, nil
		case errors.As(err, &ee) && ee.ExitCode() == 100:
		default:
			return updateCount{}, fmt.Errorf("dnf: %w", err)
		}
		c := updateCount{Pending: parseDnfCheckUpdate(out)}
		if sec, err := run(ctx, 2*time.Minute, "dnf", "-q", "updateinfo", "list", "--security"); err == nil {
			c.Security = min(parseDnfSecurity(sec), c.Pending)
		}
		return c, nil
	}
	return updateCount{}, errNoPackageManager
}

var errNoPackageManager = errors.New("kein unterstützter Paketmanager")

// rebootRequired: Debian/Ubuntu flag file written by update-notifier / unattended-upgrades.
func rebootRequired() bool {
	_, err := os.Stat("/var/run/reboot-required")
	return err == nil
}

func updatesCheck(ctx context.Context, p *periodic[updateCount], waitAll bool) (model.Check, bool) {
	// apt-get -s usually takes a few seconds: wait a little so the first report already has
	// the count, but never so long that a hanging package manager delays the report (the
	// server marks the agent unreachable after 3 missed intervals).
	// "pk95agent once" (waitAll) waits for the result.
	wctx, cancel := ctx, context.CancelFunc(func() {})
	if !waitAll {
		wctx, cancel = context.WithTimeout(ctx, updatesWait)
	}
	defer cancel()
	c, err, have := p.Get(wctx, true)
	if errors.Is(err, errNoPackageManager) || !have {
		return model.Check{}, false
	}
	return buildUpdatesCheck(c, err, rebootRequired()), true
}
