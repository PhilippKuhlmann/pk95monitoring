package agent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"pk95monitoring/internal/eventrules"
	"pk95monitoring/internal/model"
)

// platformState holds the Windows caches. Update search and event log query run in the
// background so a slow search never delays a report.
type platformState struct {
	updates *periodic[updateCount]
	events  *periodic[map[string]eventrules.Result]

	evMu      sync.Mutex
	evQueries []eventrules.Query
	evKey     string // JSON of evQueries
}

func platformMachineID() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("MachineGuid")
	if err != nil {
		return ""
	}
	return v
}

func (c *Collector) platformChecks(ctx context.Context, cfg Config, h HostInfo) []model.Check {
	if c.plat.updates == nil {
		c.plat.updates = newPeriodic(6*time.Hour, 30*time.Minute, 10*time.Minute, windowsUpdates)
		c.plat.events = newPeriodic(5*time.Minute, 2*time.Minute, 3*time.Minute, c.plat.queryEvents)
	}
	out := serviceChecks(cfg, h.Uptime)
	u, err, have := c.plat.updates.Get(ctx, c.WaitSlow)
	switch {
	case have:
		out = append(out, buildUpdatesCheck(u, err, rebootPending()))
	case rebootPending():
		out = append(out, model.Check{Key: "updates", Name: "Updates", Group: groupUpdates, State: model.StateWarn,
			Message: "Neustart erforderlich – Updatesuche läuft"})
	default:
		out = append(out, model.Check{Key: "updates", Name: "Updates", Group: groupUpdates, State: model.StateUnknown,
			Message: "Updatesuche läuft"})
	}
	if len(cfg.EventRules) > 0 {
		c.plat.setQueries(eventrules.Queries(cfg.EventRules, cfg.EventIgnore))
		if res, err, have := c.plat.events.Get(ctx, c.WaitSlow); have {
			if err != nil {
				res = map[string]eventrules.Result{}
				for _, r := range cfg.EventRules {
					res[r.ID] = eventrules.Result{Err: err.Error()}
				}
			}
			res = dropSelfRestarts(res, cfg.SelfRestarts)
			out = append(out, eventrules.Evaluate(cfg.EventRules, cfg.EventIgnore, res, time.Now())...)
		}
	}
	return out
}

// powershell runs a script hidden (no console window) and returns its standard output.
func powershell(ctx context.Context, script string) (string, error) {
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", encodePS(script))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return string(out), fmt.Errorf("%w: %s", err, truncate(string(ee.Stderr), 300))
		}
		return string(out), err
	}
	return string(out), nil
}

// psUpdateSearch queries Windows Update over COM (the same API the settings app uses): the
// pending updates and, from the update history, the last successful installation and the
// updates whose last attempt in the past 30 days failed. Definition updates (Defender)
// install several times a day and are left out of the history.
const psUpdateSearch = `$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [Text.Encoding]::UTF8
$session = New-Object -ComObject Microsoft.Update.Session
$searcher = $session.CreateUpdateSearcher()
$result = $searcher.Search("IsInstalled=0 and IsHidden=0 and Type='Software'")
$pending = 0; $security = 0; $titles = @()
foreach ($u in $result.Updates) {
  $pending++
  if ($u.MsrcSeverity -eq 'Critical' -or $u.MsrcSeverity -eq 'Important') { $security++ }
  if ($titles.Count -lt 10) { $titles += [string]$u.Title }
}
$lastInstall = 0; $failed = 0
try {
  $epoch = [datetime]'1970-01-01'
  $since = (Get-Date).ToUniversalTime().AddDays(-30)
  $defs = 'e0789628-ce08-4437-be74-2495b842f43b'
  $seen = @{}
  $n = $searcher.GetTotalHistoryCount()
  if ($n -gt 0) {
    foreach ($e in $searcher.QueryHistory(0, [Math]::Min($n, 300))) {
      if ($e.Operation -ne 1) { continue }
      $isDef = "$($e.Title)" -match 'KB2267602|Security Intelligence|Definition'
      try { foreach ($c in $e.Categories) { if ($c.CategoryID -eq $defs) { $isDef = $true } } } catch {}
      if ($isDef) { continue }
      if (($e.ResultCode -eq 2 -or $e.ResultCode -eq 3) -and $lastInstall -eq 0) { $lastInstall = [int64]($e.Date - $epoch).TotalSeconds }
      $id = "$($e.UpdateIdentity.UpdateID)"
      if (-not $seen.ContainsKey($id)) {
        $seen[$id] = $true
        if ($e.ResultCode -eq 4 -and $e.Date -ge $since) { $failed++ }
      }
    }
  }
} catch {}
Write-Output (@{ pending = $pending; security = $security; titles = $titles; last_install = $lastInstall; failed = $failed } | ConvertTo-Json -Compress)
`

func windowsUpdates(ctx context.Context) (updateCount, error) {
	out, err := powershell(ctx, psUpdateSearch)
	if err != nil {
		return updateCount{}, fmt.Errorf("Windows Update: %w", err)
	}
	var c updateCount
	if err := parsePSJSON(out, &c); err != nil {
		return updateCount{}, fmt.Errorf("Windows Update: unerwartete Ausgabe %q", truncate(out, 100))
	}
	return c, nil
}

// rebootPending checks the flags Windows Update and component servicing leave behind.
func rebootPending() bool {
	for _, p := range []string{
		`SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired`,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`,
	} {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, p, registry.QUERY_VALUE|registry.WOW64_64KEY)
		if err == nil {
			k.Close()
			return true
		}
	}
	return false
}
