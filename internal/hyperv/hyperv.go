// Package hyperv turns the Hyper-V state of a Windows host into checks: one check per VM
// (running, heartbeat, CPU and memory, the age of its oldest checkpoint) and one per replicated
// VM. The agent runs Script with PowerShell and hands the output to Parse; the demo builds a
// Report directly. The package is platform independent so it can be tested anywhere.
package hyperv

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"pk95monitoring/internal/model"
)

// Check key prefixes and groups.
const (
	KeyHost  = "hv:host"
	KeyVM    = "hv:vm:"   // + VM ID (GUID, stable across renames)
	KeyRepl  = "hv:repl:" // + VM ID
	groupVMs = "Hyper-V"
	groupRep = "Hyper-V Replikation"
)

// Script lists the VMs of the local Hyper-V host as JSON. Without the Hyper-V module it prints
// {"available":false}. Enum values (State, Heartbeat, AutomaticStartAction, replication
// state, health and mode) are English on every Windows language; Status is localized and only
// shown. Replica checkpoints (recovery points of a Replica VM) are not counted.
const Script = `$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [Text.Encoding]::UTF8
if (-not (Get-Command Get-VM -ErrorAction SilentlyContinue)) { Write-Output '{"available":false}'; exit 0 }
$epoch = [datetime]'1970-01-01'
$ts = { param($d) if ($d -and $d.Year -gt 1970) { [int64](($d.ToUniversalTime() - $epoch).TotalSeconds) } else { 0 } }
$vmList = @(Get-VM)
$snaps = @{}
foreach ($s in @($vmList | Get-VMSnapshot -ErrorAction SilentlyContinue)) {
  if ("$($s.SnapshotType)" -like '*Replica*') { continue }
  $k = "$($s.VMId)"
  if (-not $snaps.ContainsKey($k)) { $snaps[$k] = @() }
  $snaps[$k] += $s
}
$repl = @{}
foreach ($r in @(Get-VMReplication -ErrorAction SilentlyContinue)) {
  $repl["$($r.VMId)"] = @{ state = "$($r.State)"; health = "$($r.Health)"; mode = "$($r.Mode)";
    primary = "$($r.PrimaryServer)"; replica = "$($r.ReplicaServer)"; last = (& $ts $r.LastReplicationTime) }
}
$vms = @()
foreach ($v in $vmList) {
  $id = "$($v.VMId)"
  $cp = @($snaps[$id] | Where-Object { $_ })
  $oldest = 0
  if ($cp.Count -gt 0) { $oldest = & $ts (($cp | Sort-Object CreationTime | Select-Object -First 1).CreationTime) }
  $vms += @{ id = $id; name = $v.Name; state = "$($v.State)"; status = "$($v.Status)"; heartbeat = "$($v.Heartbeat)";
    auto_start = "$($v.AutomaticStartAction)"; cpu = [double]$v.CPUUsage; procs = [int]$v.ProcessorCount;
    mem = [double]$v.MemoryAssigned; demand = [double]$v.MemoryDemand; startup = [double]$v.MemoryStartup;
    dynamic = [bool]$v.DynamicMemoryEnabled; uptime = [int64]$v.Uptime.TotalSeconds; generation = [int]$v.Generation;
    version = "$($v.Version)"; clustered = [bool]$v.IsClustered; checkpoints = $cp.Count; oldest_checkpoint = $oldest;
    replication = $repl[$id] }
}
Write-Output (@{ available = $true; host = $env:COMPUTERNAME; vms = $vms } | ConvertTo-Json -Depth 4 -Compress)
`

// Report is the parsed output of Script.
type Report struct {
	Available bool   `json:"available"`
	Host      string `json:"host"`
	VMs       []VM   `json:"vms"`
}

// VM is one virtual machine.
type VM struct {
	ID               string       `json:"id"`
	Name             string       `json:"name"`
	State            string       `json:"state"`  // Running, Off, Saved, Paused, PausedCritical, Starting …
	Status           string       `json:"status"` // localized text ("Wird normal ausgeführt")
	Heartbeat        string       `json:"heartbeat"`
	AutoStart        string       `json:"auto_start"` // Nothing, StartIfRunning, Start
	CPU              float64      `json:"cpu"`        // % of the assigned vCPUs
	Procs            int          `json:"procs"`
	Mem              float64      `json:"mem"`     // assigned bytes
	Demand           float64      `json:"demand"`  // bytes the guest needs
	Startup          float64      `json:"startup"` // configured (startup) bytes
	Dynamic          bool         `json:"dynamic"`
	Uptime           int64        `json:"uptime"` // s
	Generation       int          `json:"generation"`
	Version          string       `json:"version"`
	Clustered        bool         `json:"clustered"`
	Checkpoints      int          `json:"checkpoints"`
	OldestCheckpoint int64        `json:"oldest_checkpoint"` // unix
	Replication      *Replication `json:"replication"`
}

// Replication is the replication of a VM (Hyper-V Replica).
type Replication struct {
	State   string `json:"state"`  // Replicating, Suspended, Error, WaitingForInitialReplication …
	Health  string `json:"health"` // Normal, Warning, Critical, NotApplicable
	Mode    string `json:"mode"`   // Primary, Replica, ExtendedReplica, TestReplica
	Primary string `json:"primary"`
	Replica string `json:"replica"`
	Last    int64  `json:"last"` // unix of the last replication
}

// Parse decodes the output of Script; ConvertTo-Json writes a single VM as an object instead
// of a list, which Parse accepts too.
func Parse(out string) (Report, error) {
	s := strings.TrimSpace(out)
	if i := strings.IndexByte(s, '{'); i > 0 {
		s = s[i:]
	}
	if i := strings.LastIndexByte(s, '}'); i >= 0 && i < len(s)-1 {
		s = s[:i+1]
	}
	if s == "" {
		return Report{}, errors.New("keine Ausgabe")
	}
	var raw struct {
		Available bool            `json:"available"`
		Host      string          `json:"host"`
		VMs       json.RawMessage `json:"vms"`
	}
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return Report{}, err
	}
	r := Report{Available: raw.Available, Host: raw.Host}
	v := strings.TrimSpace(string(raw.VMs))
	switch {
	case v == "" || v == "null":
	case strings.HasPrefix(v, "{"):
		var one VM
		if err := json.Unmarshal(raw.VMs, &one); err != nil {
			return Report{}, err
		}
		r.VMs = []VM{one}
	default:
		if err := json.Unmarshal(raw.VMs, &r.VMs); err != nil {
			return Report{}, err
		}
	}
	return r, nil
}

// replica reports whether the VM is a copy on a Replica server: it is off by design.
func (v VM) replica() bool {
	return v.Replication != nil && v.Replication.Mode != "" && v.Replication.Mode != "Primary"
}

// mustRun reports whether a VM that is not running is a problem: it starts with the host
// (AutomaticStartAction Start or StartIfRunning) and is not a Replica copy. Single VMs are
// switched off or made quiet with check rules (hv:vm:<id>).
func (v VM) mustRun() bool { return !v.replica() && v.AutoStart != "" && v.AutoStart != "Nothing" }

var autoStartText = map[string]string{"Nothing": "nein", "StartIfRunning": "wenn sie lief", "Start": "immer"}

// Checks builds the checks of a report: hv:host (how many VMs run; problems are judged per VM,
// partly by server thresholds, so the host check does not count them), one hv:vm:<id> per VM
// and one hv:repl:<id> per replicated VM. A host without Hyper-V has no checks; a failed query is one unknown
// hv:host.
func Checks(r Report, err error, now time.Time) []model.Check {
	if err != nil {
		return []model.Check{{Key: KeyHost, Name: "Hyper-V", Group: groupVMs, State: model.StateUnknown,
			Message: "Hyper-V nicht lesbar: " + err.Error()}}
	}
	if !r.Available {
		return nil
	}
	vms := slices.Clone(r.VMs)
	slices.SortFunc(vms, func(a, b VM) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	running := 0
	var rest []model.Check
	for _, v := range vms {
		if v.State == "Running" {
			running++
		}
		rest = append(rest, vmCheck(v, now))
		if rc, ok := replCheck(v, now); ok {
			rest = append(rest, rc)
		}
	}
	host := model.Check{Key: KeyHost, Name: "Hyper-V", Group: groupVMs, State: model.StateOK,
		Message: fmt.Sprintf("%d von %d %s laufen", running, len(vms), plural(len(vms), "VM", "VMs")),
		Facts:   map[string]string{"VMs": strconv.Itoa(len(vms)), "Laufen": strconv.Itoa(running)}}
	return append([]model.Check{host}, rest...)
}

func vmCheck(v VM, now time.Time) model.Check {
	ch := model.Check{Key: KeyVM + v.ID, Name: "VM " + v.Name, Group: groupVMs, State: model.StateOK, Facts: map[string]string{}}
	inst := v.Name
	if v.Procs > 0 {
		ch.Facts["vCPUs"] = strconv.Itoa(v.Procs)
	}
	if v.Startup > 0 {
		ram := fmtBytes(v.Startup)
		if v.Dynamic {
			ram += " (dynamisch)"
		}
		ch.Facts["RAM"] = ram
	}
	if v.Generation > 0 {
		ch.Facts["Generation"] = strconv.Itoa(v.Generation)
	}
	if v.Version != "" {
		ch.Facts["Konfigurationsversion"] = v.Version
	}
	if t, ok := autoStartText[v.AutoStart]; ok {
		ch.Facts["Autostart"] = t
	}
	if v.Clustered {
		ch.Facts["Cluster"] = "ja"
	}
	if v.replica() {
		ch.Facts["Replikat"] = "ja"
	}
	ch.Facts["Prüfpunkte"] = strconv.Itoa(v.Checkpoints)
	switch v.State {
	case "Running":
		ch.Message = "läuft"
		if v.Uptime > 0 {
			ch.Message += " seit " + fmtDuration(time.Duration(v.Uptime)*time.Second)
		}
		ch.Message += fmt.Sprintf(" · CPU %.0f %% · RAM %s", v.CPU, fmtBytes(v.Mem))
		ch.Metrics = []model.Metric{
			{Name: "guest.cpu", Instance: inst, Value: round2(v.CPU), Unit: "%"},
			{Name: "guest.mem_used", Instance: inst, Value: v.Mem, Unit: "B"},
		}
		if v.Demand > 0 && v.Mem > 0 {
			ch.Metrics = append(ch.Metrics, model.Metric{Name: "guest.mem", Instance: inst, Value: round2(100 * v.Demand / v.Mem), Unit: "%"})
		}
		if v.Heartbeat == "LostCommunication" {
			ch.State = model.StateWarn
			ch.Message = "Heartbeat verloren – das Gastsystem antwortet nicht (hängt oder startet neu) · " + ch.Message
		}
	case "PausedCritical", "Paused":
		ch.State = model.StateCrit
		ch.Message = "angehalten"
		if v.State == "PausedCritical" {
			ch.Message = "angehalten – meist ist der Datenträger mit den VM-Dateien voll"
		}
	case "Starting", "Stopping", "Saving", "Pausing", "Resuming", "Reset", "FastSaving", "Other":
		ch.Message = "Status " + v.State
	default: // Off, Saved
		what := "aus"
		if v.State == "Saved" {
			what = "gespeichert (nicht gestartet)"
		}
		switch {
		case v.replica():
			ch.Message = "Replikat – " + what
		case v.mustRun():
			ch.State = model.StateCrit
			ch.Message = "VM läuft nicht – " + what + " (startet mit dem Host)"
		default:
			ch.Message = what
		}
	}
	if v.Status != "" && v.State != "Running" && v.State != "Off" {
		ch.Facts["Status"] = v.Status
	}
	if v.Checkpoints > 0 && v.OldestCheckpoint > 0 {
		days := max(0, now.Sub(time.Unix(v.OldestCheckpoint, 0)).Hours()/24)
		ch.Metrics = append(ch.Metrics, model.Metric{Name: "checkpoint.age", Instance: inst, Value: round2(days), Unit: "d"})
		ch.Facts["Ältester Prüfpunkt"] = time.Unix(v.OldestCheckpoint, 0).Local().Format("02.01.2006 15:04")
		ch.Message += fmt.Sprintf(" · %d %s (ältester vor %s)", v.Checkpoints, plural(v.Checkpoints, "Prüfpunkt", "Prüfpunkte"), fmtDays(days))
	}
	return ch
}

var replModeText = map[string]string{"Primary": "Primär", "Replica": "Replikat", "ExtendedReplica": "Erweitertes Replikat", "TestReplica": "Testreplikat"}

func replCheck(v VM, now time.Time) (model.Check, bool) {
	r := v.Replication
	if r == nil || r.State == "" || r.State == "Disabled" {
		return model.Check{}, false
	}
	ch := model.Check{Key: KeyRepl + v.ID, Name: "Replikation " + v.Name, Group: groupRep, State: model.StateOK,
		Facts: map[string]string{"Status": r.State, "Integrität": r.Health}}
	if t, ok := replModeText[r.Mode]; ok {
		ch.Facts["Modus"] = t
	}
	if r.Primary != "" {
		ch.Facts["Primärserver"] = r.Primary
	}
	if r.Replica != "" {
		ch.Facts["Replikatserver"] = r.Replica
	}
	target := r.Replica
	if r.Mode != "Primary" {
		target = r.Primary
	}
	when := ""
	if r.Last > 0 {
		age := max(0, now.Sub(time.Unix(r.Last, 0)))
		ch.Facts["Letzte Replikation"] = time.Unix(r.Last, 0).Local().Format("02.01.2006 15:04")
		ch.Metrics = []model.Metric{{Name: "repl.age", Instance: v.Name, Value: round2(age.Minutes()), Unit: "min"}}
		when = " · zuletzt vor " + fmtDuration(age)
	}
	switch {
	case r.Health == "Critical" || r.State == "Error" || r.State == "Critical":
		ch.State = model.StateCrit
		ch.Message = "Replikation kritisch (Status " + r.State + ")"
	case r.State == "Suspended":
		ch.State = model.StateWarn
		ch.Message = "Replikation angehalten"
	case r.Health == "Warning" || r.State == "Resynchronizing" || r.State == "ResynchronizeSuspended":
		ch.State = model.StateWarn
		ch.Message = "Replikation mit Warnung (Status " + r.State + ")"
	case r.State == "WaitingForInitialReplication" || r.State == "InitialReplicationInProgress":
		ch.Message = "Erstreplikation läuft"
	default:
		ch.Message = "Repliziert"
	}
	if target != "" {
		ch.Message += " · " + target
	}
	ch.Message += when
	return ch, true
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }

func fmtBytes(b float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	i := 0
	for b >= 1024 && i < len(units)-1 {
		b /= 1024
		i++
	}
	if i == 0 || b >= 100 || b == float64(int64(b)) {
		return fmt.Sprintf("%.0f %s", b, units[i])
	}
	return strings.Replace(fmt.Sprintf("%.1f %s", b, units[i]), ".", ",", 1)
}

func fmtDuration(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return strconv.Itoa(int(d.Hours()/24)) + " Tagen"
	case d >= 24*time.Hour:
		return "1 Tag"
	case d >= time.Hour:
		return strconv.Itoa(int(d.Hours())) + " Std."
	}
	return strconv.Itoa(max(1, int(d.Minutes()))) + " Min."
}

func fmtDays(days float64) string {
	if days < 1 {
		return fmtDuration(time.Duration(days * 24 * float64(time.Hour)))
	}
	return fmtDuration(time.Duration(days) * 24 * time.Hour)
}
