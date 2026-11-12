package agent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"pk95monitoring/internal/model"
)

// platformState holds the Windows caches. Update search and event log query run in the
// background so a slow search never delays a report.
type platformState struct {
	updates  *periodic[updateCount]
	eventlog *periodic[eventlogResult]
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
		c.plat.eventlog = newPeriodic(15*time.Minute, 5*time.Minute, 2*time.Minute, eventlogErrors)
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
	if ev, err, have := c.plat.eventlog.Get(ctx, c.WaitSlow); have {
		out = append(out, eventlogCheck(ev, err))
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

// psUpdateSearch queries Windows Update over COM (the same API the settings app uses).
const psUpdateSearch = `$ErrorActionPreference = 'Stop'
$session = New-Object -ComObject Microsoft.Update.Session
$searcher = $session.CreateUpdateSearcher()
$result = $searcher.Search("IsInstalled=0 and IsHidden=0 and Type='Software'")
$pending = 0; $security = 0
foreach ($u in $result.Updates) {
  $pending++
  if ($u.MsrcSeverity -eq 'Critical' -or $u.MsrcSeverity -eq 'Important') { $security++ }
}
Write-Output (@{ pending = $pending; security = $security } | ConvertTo-Json -Compress)
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

// psEventlog counts Error/Critical events of the last 24 h and Critical ones of the last hour.
const psEventlog = `$ErrorActionPreference = 'SilentlyContinue'
[Console]::OutputEncoding = [Text.Encoding]::UTF8
$day = (Get-Date).AddHours(-24)
$hour = (Get-Date).AddHours(-1)
$events = @(Get-WinEvent -FilterHashtable @{ LogName = 'System','Application'; Level = 1,2; StartTime = $day })
$crit = @($events | Where-Object { $_.Level -eq 1 -and $_.TimeCreated -ge $hour } | Sort-Object TimeCreated -Descending)
$last = ''
if ($crit.Count -gt 0) {
  $msg = "$($crit[0].Message)" -split "\r?\n" | Select-Object -First 1
  $last = "$($crit[0].ProviderName) ($($crit[0].Id)): $msg"
}
Write-Output (@{ errors = $events.Count; critical = $crit.Count; last = $last } | ConvertTo-Json -Compress)
`

func eventlogErrors(ctx context.Context) (eventlogResult, error) {
	out, err := powershell(ctx, psEventlog)
	if err != nil {
		return eventlogResult{}, fmt.Errorf("Ereignisprotokoll: %w", err)
	}
	var r eventlogResult
	if err := parsePSJSON(out, &r); err != nil {
		return eventlogResult{}, fmt.Errorf("Ereignisprotokoll: unerwartete Ausgabe %q", truncate(out, 100))
	}
	return r, nil
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
