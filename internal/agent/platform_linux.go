package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pk95monitoring/internal/eventrules"
	"pk95monitoring/internal/model"
)

// updatesWait is how long a report waits for a running update search.
const updatesWait = 20 * time.Second

// platformState holds the Linux caches. The update search and the journal queries run in
// the background so a slow one never delays a report.
type platformState struct {
	updates *periodic[updateCount]
	events  *periodic[map[string]eventrules.Result]

	evMu      sync.Mutex
	evQueries []eventrules.Query
	evKey     string      // JSON of evQueries
	noGrep    atomic.Bool // journalctl was built without pattern matching
}

func platformMachineID() string {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) >= 16 {
			return string(b)
		}
	}
	return ""
}

func (c *Collector) platformChecks(ctx context.Context, cfg Config, h HostInfo) []model.Check {
	var out []model.Check
	if systemdBooted() {
		out = append(out, systemdCheck(ctx))
		out = append(out, serviceChecks(ctx, cfg.Services)...)
		out = append(out, patternServiceChecks(ctx, cfg, h.Uptime)...)
	}
	if c.plat.updates == nil {
		c.plat.updates = newPeriodic(6*time.Hour, 30*time.Minute, 5*time.Minute, linuxUpdates)
	}
	if ch, ok := updatesCheck(ctx, c.plat.updates, c.WaitSlow); ok {
		out = append(out, ch)
	}
	if len(cfg.EventRules) > 0 && systemdBooted() {
		if c.plat.events == nil {
			c.plat.events = newPeriodic(5*time.Minute, 2*time.Minute, 3*time.Minute, c.plat.queryEvents)
		}
		c.plat.setQueries(eventrules.Queries(cfg.EventRules, cfg.EventIgnore))
		if res, err, have := c.plat.events.Get(ctx, c.WaitSlow); have {
			if err != nil {
				res = map[string]eventrules.Result{}
				for _, r := range cfg.EventRules {
					res[r.ID] = eventrules.Result{Err: err.Error()}
				}
			}
			out = append(out, eventrules.Evaluate(cfg.EventRules, cfg.EventIgnore, res, time.Now())...)
		}
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

// serviceChecks reports every configured unit name (not the patterns); it must be active.
func serviceChecks(ctx context.Context, services []string) []model.Check {
	var units []string
	for _, s := range services {
		if s = strings.TrimSpace(s); s != "" && !isPattern(s) {
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

// patternServiceChecks reports the units matched by the patterns of the device's list and by
// the central list: every matching unit that is active or enabled, except ignored ones and
// units the device lists by name (serviceChecks reports those).
func patternServiceChecks(ctx context.Context, cfg Config, uptime uint64) []model.Check {
	var patterns []string
	named := map[string]bool{}
	for _, s := range cfg.Services {
		if s = strings.TrimSpace(s); isPattern(s) {
			patterns = append(patterns, s)
		} else if s != "" {
			named[strings.ToLower(svcBase(s))] = true
		}
	}
	patterns = append(patterns, cfg.CentralServices...)
	if len(patterns) == 0 {
		return nil
	}
	lu, err := run(ctx, 30*time.Second, "systemctl", "list-units", "--type=service", "--all", "--no-legend", "--plain", "--no-pager")
	if err != nil {
		return []model.Check{{Key: "services", Name: "Dienste", Group: groupServices, State: model.StateUnknown, Message: "systemctl: " + err.Error()}}
	}
	uf, err := run(ctx, 30*time.Second, "systemctl", "list-unit-files", "--type=service", "--no-legend", "--no-pager")
	if err != nil {
		uf = "" // without the unit files only active units match
	}
	units, enabled := parseListUnits(lu), parseEnabledUnits(uf)
	entries := make([]svcEntry, 0, len(units)+len(enabled))
	for name, u := range units {
		entries = append(entries, svcEntry{Name: name, Display: u.Description, Active: u.Active == "active"})
	}
	for name := range enabled {
		if _, ok := units[name]; !ok {
			entries = append(entries, svcEntry{Name: name}) // enabled but not loaded: not running
		}
	}
	var out []model.Check
	for _, p := range pickServices(entries, nil, patterns, cfg.IgnoreServices, func(e svcEntry) bool { return enabled[e.Name] }) {
		if named[strings.ToLower(svcBase(p.Entry.Name))] {
			continue
		}
		st := units[p.Entry.Name].Active
		if st == "" {
			st = "inactive"
		}
		ch := model.Check{Key: p.Key, Name: "Dienst " + svcBase(p.Entry.Name), Group: groupServices, Message: unitStateText(st)}
		if d := units[p.Entry.Name].Description; d != "" {
			ch.Facts = map[string]string{"Beschreibung": d, "Unit": p.Entry.Name}
		}
		switch {
		case st == "active":
			ch.State = model.StateOK
		case uptime < bootGrace:
			ch.State, ch.Message = model.StateUnknown, "startet nach dem Systemstart noch ("+ch.Message+")"
		case st == "activating" || st == "reloading":
			ch.State = model.StateWarn
		default:
			ch.State = model.StateCrit
		}
		out = append(out, ch)
	}
	return out
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
