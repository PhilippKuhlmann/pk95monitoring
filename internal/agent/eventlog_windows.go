package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"pk95monitoring/internal/eventrules"
)

// queryEvents runs the event log queries of the rules. The queries go through a temporary
// file: the command line of powershell.exe is limited to 32 767 characters.
func (p *platformState) queryEvents(ctx context.Context) (map[string]eventrules.Result, error) {
	p.evMu.Lock()
	q := p.evQueries
	p.evMu.Unlock()
	if len(q) == 0 {
		return map[string]eventrules.Result{}, nil
	}
	b, err := json.Marshal(map[string]any{"queries": q})
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp("", "pk95agent-events-*.json")
	if err != nil {
		return nil, fmt.Errorf("Ereignisprotokoll: %w", err)
	}
	defer os.Remove(f.Name())
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("Ereignisprotokoll: %w", err)
	}
	script := strings.Replace(psEvents, "{{PATH}}", strings.ReplaceAll(f.Name(), "'", "''"), 1)
	out, err := powershell(ctx, script)
	if err != nil {
		return nil, fmt.Errorf("Ereignisprotokoll: %w", err)
	}
	res, err := parseEventlog(out)
	if err != nil {
		return nil, fmt.Errorf("Ereignisprotokoll: unerwartete Ausgabe %q", truncate(out, 100))
	}
	return res, nil
}

// psEvents reads the events of each query (newest first, at most max per log) with their
// message: of every event when a text filter needs it, otherwise of the newest ten. Logs that
// do not exist on this host (no Veeam, no DFS) and queries without hits give no events.
const psEvents = `$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [Text.Encoding]::UTF8
$in = Get-Content -Raw -Encoding UTF8 -LiteralPath '{{PATH}}' | ConvertFrom-Json
$epoch = [datetime]'1970-01-01'
$logs = @{}
$out = @()
foreach ($q in @($in.queries)) {
  $events = @(); $full = $false; $err = ''
  foreach ($log in @($q.logs)) {
    $name = [string]$log
    if (-not $logs.ContainsKey($name)) { $logs[$name] = [bool](Get-WinEvent -ListLog $name -ErrorAction SilentlyContinue) }
    if (-not $logs[$name]) { continue }
    $found = @()
    try {
      $found = @(Get-WinEvent -LogName $name -FilterXPath ([string]$q.xpath) -MaxEvents ([int]$q.max) -ErrorAction Stop)
    } catch {
      if ("$($_.FullyQualifiedErrorId)" -notlike 'NoMatchingEventsFound*') { $err = "${name}: $($_.Exception.Message)" }
    }
    if ($found.Count -ge [int]$q.max) { $full = $true }
    $n = 0
    foreach ($e in $found) {
      $msg = ''
      if ($q.text -or $n -lt 10) {
        try { $msg = [string]$e.Message } catch { $msg = '' }
        if ($msg.Length -gt 1000) { $msg = $msg.Substring(0, 1000) }
      }
      $events += @{ t = [int64]($e.TimeCreated.ToUniversalTime() - $epoch).TotalSeconds; log = $name; src = [string]$e.ProviderName; id = [int]$e.Id; lvl = [int]$e.Level; msg = $msg }
      $n++
    }
  }
  $out += @{ id = [string]$q.id; events = $events; full = $full; err = $err }
}
Write-Output (ConvertTo-Json -InputObject @{ rules = $out } -Compress -Depth 5)
`
