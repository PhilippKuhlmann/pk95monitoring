// Package addc turns the state of an Active Directory domain controller into checks: the
// domain controller itself (domain, site, global catalog, FSMO roles), the inbound replication
// from every partner DC, SYSVOL replication and shares, the AD services and the clock offset to
// the PDC emulator. The agent runs Script with PowerShell on Windows servers and hands the
// output to Parse; the demo builds a Report directly. The package is platform independent.
package addc

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"pk95monitoring/internal/model"
)

// Check keys and group.
const (
	KeyDC       = "ad:dc"
	KeyRepl     = "ad:repl:" // + partner DC name (lower case)
	KeySysvol   = "ad:sysvol"
	KeyServices = "ad:services"
	KeyTime     = "ad:time"
	group       = "Active Directory"
)

// Script reads the domain controller state as JSON; on a server that is no domain controller
// it prints {"available":false}. It needs the ActiveDirectory module, which the AD DS role
// installs. Enum values are English on every Windows language; w32tm prints the offset as a
// number with "s".
const Script = `$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [Text.Encoding]::UTF8
$role = (Get-CimInstance Win32_ComputerSystem).DomainRole
if ($role -lt 4) { Write-Output '{"available":false}'; exit 0 }
$epoch = [datetime]'1970-01-01'
$ts = { param($d) if ($d -and $d.Year -gt 1970) { [int64](($d.ToUniversalTime() - $epoch).TotalSeconds) } else { 0 } }
$out = @{ available = $true; host = $env:COMPUTERNAME; errors = @() }
try {
  Import-Module ActiveDirectory -ErrorAction Stop
  $dom = Get-ADDomain; $for = Get-ADForest
  $dc = Get-ADDomainController -Identity $env:COMPUTERNAME
  $out.domain = "$($dom.DNSRoot)"; $out.forest = "$($for.Name)"; $out.site = "$($dc.Site)"; $out.gc = [bool]$dc.IsGlobalCatalog
  $out.rodc = [bool]$dc.IsReadOnly; $out.hostname = "$($dc.HostName)"
  $out.domain_mode = "$($dom.DomainMode)"; $out.forest_mode = "$($for.ForestMode)"
  $out.pdc = "$($dom.PDCEmulator)"; $out.rid = "$($dom.RIDMaster)"; $out.infra = "$($dom.InfrastructureMaster)"
  $out.schema = "$($for.SchemaMaster)"; $out.naming = "$($for.DomainNamingMaster)"
  $repl = @()
  foreach ($p in @(Get-ADReplicationPartnerMetadata -Target $env:COMPUTERNAME -PartnerType Inbound -Scope Server -Partition * -ErrorAction Stop)) {
    $repl += @{ partner = "$($p.Partner)"; partition = "$($p.Partition)"; last_success = (& $ts $p.LastReplicationSuccess);
      last_attempt = (& $ts $p.LastReplicationAttempt); result = [int64]$p.LastReplicationResult; failures = [int]$p.ConsecutiveReplicationFailures }
  }
  $out.replication = $repl
} catch { $out.errors += "$($_.Exception.Message)" }
$svc = @{}
foreach ($n in 'NTDS','Netlogon','Kdc','DNS','DFSR','ADWS','W32Time','NtFrs') {
  $s = Get-Service -Name $n -ErrorAction SilentlyContinue
  if ($s) { $svc[$n] = @{ status = "$($s.Status)"; start = "$($s.StartType)"; display = "$($s.DisplayName)" } }
}
$out.services = $svc
$sv = @{ state = -1; migration = -1 }
try {
  $r = Get-CimInstance -Namespace 'root\microsoftdfs' -ClassName DfsrReplicatedFolderInfo -Filter "ReplicatedFolderName='SYSVOL Share'" -ErrorAction Stop | Select-Object -First 1
  if ($r) { $sv.state = [int]$r.State }
} catch {}
try { $sv.migration = [int](Get-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Services\DFSR\Parameters\SysVols\Migrating Sysvols' -ErrorAction Stop).LocalState } catch {}
$sv.shares = @(Get-SmbShare -ErrorAction SilentlyContinue | Where-Object { $_.Name -in 'SYSVOL','NETLOGON' } | ForEach-Object { $_.Name })
$out.sysvol = $sv
$t = @{ source = ""; checked = $false }
try { $t.source = "$((& w32tm /query /source 2>$null | Select-Object -First 1))".Trim() } catch {}
if ($out.pdc -and $out.hostname -and $out.pdc -ne $out.hostname) {
  $t.checked = $true
  foreach ($l in @(& w32tm /stripchart /computer:$($out.pdc) /samples:1 /dataonly 2>$null)) {
    if ("$l" -match '([+-]\d+[\.,]\d+)s\s*$') { $t.offset = [double]($matches[1] -replace ',', '.') }
  }
}
$out.time = $t
Write-Output ($out | ConvertTo-Json -Depth 5 -Compress)
`

// Report is the parsed output of Script.
type Report struct {
	Available   bool               `json:"available"`
	Host        string             `json:"host"`
	Errors      []string           `json:"errors"`
	Domain      string             `json:"domain"`
	Forest      string             `json:"forest"`
	Site        string             `json:"site"`
	GC          bool               `json:"gc"`
	RODC        bool               `json:"rodc"`
	Hostname    string             `json:"hostname"`
	DomainMode  string             `json:"domain_mode"`
	ForestMode  string             `json:"forest_mode"`
	PDC         string             `json:"pdc"`
	RID         string             `json:"rid"`
	Infra       string             `json:"infra"`
	Schema      string             `json:"schema"`
	Naming      string             `json:"naming"`
	Replication []Link             `json:"replication"`
	Services    map[string]Service `json:"services"`
	Sysvol      Sysvol             `json:"sysvol"`
	Time        Time               `json:"time"`
}

// Link is the inbound replication of one partition from one partner.
type Link struct {
	Partner     string `json:"partner"`   // DN of the partner's NTDS Settings
	Partition   string `json:"partition"` // DN of the naming context
	LastSuccess int64  `json:"last_success"`
	LastAttempt int64  `json:"last_attempt"`
	Result      int64  `json:"result"` // Win32 error of the last attempt, 0 = success
	Failures    int    `json:"failures"`
}

// Service is a Windows service.
type Service struct {
	Status  string `json:"status"` // Running, Stopped …
	Start   string `json:"start"`  // Automatic, Manual, Disabled
	Display string `json:"display"`
}

// Sysvol is the SYSVOL replication: State of the DFSR replicated folder "SYSVOL Share"
// (0 uninitialized, 1 initialized, 2 initial sync, 3 auto recovery, 4 normal, 5 in error,
// -1 none), Migration the FRS→DFSR migration state (3 = eliminated, -1 = never migrated).
type Sysvol struct {
	State     int      `json:"state"`
	Migration int      `json:"migration"`
	Shares    []string `json:"shares"`
}

// Time is the clock against the PDC emulator.
type Time struct {
	Source  string   `json:"source"`
	Checked bool     `json:"checked"` // the offset to the PDC emulator was measured
	Offset  *float64 `json:"offset"`  // seconds; nil = no answer
}

// Parse decodes the output of Script (single entries come as objects instead of lists).
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
		Report
		Errors      json.RawMessage `json:"errors"`
		Replication json.RawMessage `json:"replication"`
		Sysvol      struct {
			State     int             `json:"state"`
			Migration int             `json:"migration"`
			Shares    json.RawMessage `json:"shares"`
		} `json:"sysvol"`
	}
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return Report{}, err
	}
	r := raw.Report
	if err := listOrOne(raw.Errors, &r.Errors); err != nil {
		return Report{}, err
	}
	if err := listOrOne(raw.Replication, &r.Replication); err != nil {
		return Report{}, err
	}
	r.Sysvol = Sysvol{State: raw.Sysvol.State, Migration: raw.Sysvol.Migration}
	if err := listOrOne(raw.Sysvol.Shares, &r.Sysvol.Shares); err != nil {
		return Report{}, err
	}
	return r, nil
}

// listOrOne decodes a JSON list or a single element (ConvertTo-Json unwraps one-element lists).
func listOrOne[T any](b json.RawMessage, out *[]T) error {
	v := strings.TrimSpace(string(b))
	switch {
	case v == "" || v == "null":
		return nil
	case strings.HasPrefix(v, "["):
		return json.Unmarshal(b, out)
	}
	var one T
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	*out = []T{one}
	return nil
}

// dnValue returns the value of the RDN at index i ("CN=NTDS Settings,CN=DC02,…", 1 → "DC02").
func dnValue(dn string, i int) string {
	parts := strings.Split(dn, ",")
	if i >= len(parts) {
		return ""
	}
	_, v, _ := strings.Cut(parts[i], "=")
	return strings.TrimSpace(v)
}

// partnerName is the DC name in the DN of its NTDS Settings; a deleted DC is marked.
func partnerName(dn string) string {
	name := cmp.Or(dnValue(dn, 1), dn)
	if strings.Contains(dn, `\0ADEL:`) {
		return name + " (gelöscht)"
	}
	return name
}

// partitionName shortens a naming context DN.
func partitionName(dn string) string {
	switch {
	case strings.HasPrefix(dn, "CN=Schema,"):
		return "Schema"
	case strings.HasPrefix(dn, "CN=Configuration,"):
		return "Konfiguration"
	case strings.HasPrefix(dn, "DC=DomainDnsZones,"):
		return "DomainDnsZones"
	case strings.HasPrefix(dn, "DC=ForestDnsZones,"):
		return "ForestDnsZones"
	}
	var labels []string
	for _, p := range strings.Split(dn, ",") {
		if k, v, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.EqualFold(k, "DC") {
			labels = append(labels, v)
		}
	}
	if len(labels) == 0 {
		return dn
	}
	return strings.Join(labels, ".")
}

// replErrors explains the common replication errors (Win32 codes).
var replErrors = map[int64]string{
	5:           "Zugriff verweigert",
	1256:        "Remotesystem nicht erreichbar",
	1396:        "Anmeldefehler – Zielkontoname falsch (Kerberos/SPN)",
	1722:        "RPC-Server nicht verfügbar (Partner aus, Firewall oder DNS)",
	1753:        "Keine Endpunkte verfügbar (RPC-Endpunktzuordnung)",
	1908:        "Domänencontroller nicht gefunden",
	8453:        "Replikationszugriff verweigert",
	8524:        "DNS-Suche fehlgeschlagen – Partner nicht auflösbar",
	8606:        "Unzureichende Attribute (veraltete Objekte, lingering objects)",
	8614:        "Tombstone-Lebensdauer überschritten – der Partner darf nicht mehr replizieren",
	-2146893022: "Zielprinzipalname falsch (Kerberos)",
}

func replError(code int64) string {
	if t, ok := replErrors[code]; ok {
		return fmt.Sprintf("Fehler %d: %s", code, t)
	}
	return fmt.Sprintf("Fehler %d", code)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func fmtAge(d time.Duration) string {
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

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// Checks builds the checks of a report. A server that is no domain controller has none.
func Checks(r Report, err error, now time.Time) []model.Check {
	if err != nil {
		return []model.Check{{Key: KeyDC, Name: "Domänencontroller", Group: group, State: model.StateUnknown,
			Message: "Active Directory nicht lesbar: " + err.Error()}}
	}
	if !r.Available {
		return nil
	}
	out := []model.Check{dcCheck(r)}
	if r.Domain != "" { // the AD module answered
		out = append(out, replChecks(r, now)...)
	}
	return append(out, sysvolCheck(r), serviceCheck(r), timeCheck(r))
}

func shortHost(fqdn string) string {
	h, _, _ := strings.Cut(fqdn, ".")
	return strings.ToUpper(h)
}

func dcCheck(r Report) model.Check {
	ch := model.Check{Key: KeyDC, Name: "Domänencontroller", Group: group, State: model.StateOK, Facts: map[string]string{}}
	if r.Domain == "" {
		ch.State = model.StateUnknown
		ch.Message = "Active Directory nicht lesbar"
		if len(r.Errors) > 0 {
			ch.Message += ": " + r.Errors[0]
		}
		return ch
	}
	me := strings.ToLower(r.Hostname)
	var roles []string
	for _, x := range []struct{ host, name string }{{r.PDC, "PDC-Emulator"}, {r.RID, "RID-Master"}, {r.Infra, "Infrastrukturmaster"},
		{r.Schema, "Schemamaster"}, {r.Naming, "Domänennamenmaster"}} {
		if x.host != "" && strings.ToLower(x.host) == me {
			roles = append(roles, x.name)
		}
	}
	set := func(k, v string) {
		if v != "" {
			ch.Facts[k] = v
		}
	}
	set("Domäne", r.Domain)
	set("Gesamtstruktur", r.Forest)
	set("Standort", r.Site)
	set("Funktionsebene Domäne", r.DomainMode)
	set("Funktionsebene Gesamtstruktur", r.ForestMode)
	set("PDC-Emulator", shortHost(r.PDC))
	ch.Facts["Globaler Katalog"] = map[bool]string{true: "ja", false: "nein"}[r.GC]
	if r.RODC {
		ch.Facts["Schreibgeschützt (RODC)"] = "ja"
	}
	if len(roles) > 0 {
		ch.Facts["FSMO-Rollen"] = strings.Join(roles, ", ")
	}
	parts := []string{r.Domain}
	if r.Site != "" {
		parts = append(parts, "Standort "+r.Site)
	}
	if r.GC {
		parts = append(parts, "Globaler Katalog")
	}
	if len(roles) > 0 {
		parts = append(parts, "FSMO: "+strings.Join(roles, ", "))
	}
	ch.Message = strings.Join(parts, " · ")
	if len(r.Errors) > 0 {
		ch.Facts["Hinweis"] = r.Errors[0]
	}
	return ch
}

// replChecks are "ad:repl:<partner>": the inbound replication from each partner over all
// partitions. Failed attempts are a warning (tombstone lifetime exceeded: critical); the age
// of the oldest successful replication is ad.repl_age (h), judged by thresholds.
func replChecks(r Report, now time.Time) []model.Check {
	if len(r.Replication) == 0 {
		return []model.Check{{Key: KeyRepl + "-", Name: "Replikation", Group: group, State: model.StateOK,
			Message: "Kein Replikationspartner – einziger Domänencontroller der Domäne"}}
	}
	byPartner := map[string][]Link{}
	for _, l := range r.Replication {
		name := partnerName(l.Partner)
		byPartner[name] = append(byPartner[name], l)
	}
	var out []model.Check
	for _, name := range slices.Sorted(maps.Keys(byPartner)) {
		links := byPartner[name]
		key := KeyRepl + strings.ToLower(strings.Fields(name)[0])
		ch := model.Check{Key: key, Name: "Replikation von " + name, Group: group, State: model.StateOK, Facts: map[string]string{}}
		var failing, parts []string
		oldest, worstFails := int64(0), 0
		var worstCode int64
		for _, l := range links {
			p := partitionName(l.Partition)
			parts = append(parts, p)
			if l.LastSuccess > 0 && (oldest == 0 || l.LastSuccess < oldest) {
				oldest = l.LastSuccess
			}
			if l.Failures > 0 || l.Result != 0 {
				failing = append(failing, p)
				if l.Failures >= worstFails {
					worstFails, worstCode = l.Failures, l.Result
				}
			}
		}
		slices.Sort(parts)
		slices.Sort(failing)
		ch.Facts["Partitionen"] = strings.Join(parts, ", ")
		switch {
		case len(failing) > 0:
			ch.State = model.StateWarn
			if worstCode == 8614 {
				ch.State = model.StateCrit
			}
			n := max(worstFails, 1)
			ch.Message = fmt.Sprintf("%d %s fehlgeschlagen (%s) · %s", n, plural(n, "Versuch", "Versuche"), strings.Join(failing, ", "), replError(worstCode))
			ch.Facts["Fehler"] = replError(worstCode)
		default:
			ch.Message = fmt.Sprintf("%d %s in Ordnung", len(links), plural(len(links), "Partition", "Partitionen"))
		}
		if oldest > 0 {
			age := max(0, now.Sub(time.Unix(oldest, 0)))
			ch.Metrics = []model.Metric{{Name: "ad.repl_age", Instance: name, Value: round2(age.Hours()), Unit: "h"}}
			ch.Facts["Letzte erfolgreiche Replikation"] = time.Unix(oldest, 0).Local().Format("02.01.2006 15:04")
			ch.Message += " · letzte erfolgreiche Replikation vor " + fmtAge(age)
		} else {
			ch.State = model.Worst(ch.State, model.StateWarn)
			ch.Message += " · noch nie erfolgreich repliziert"
		}
		out = append(out, ch)
	}
	return out
}

var dfsrState = map[int]string{0: "nicht initialisiert", 1: "initialisiert", 2: "Erstsynchronisierung", 3: "automatische Wiederherstellung", 4: "normal", 5: "Fehler"}

// sysvolCheck is "ad:sysvol": DFSR replication of SYSVOL and the shares SYSVOL and NETLOGON.
func sysvolCheck(r Report) model.Check {
	ch := model.Check{Key: KeySysvol, Name: "SYSVOL", Group: group, State: model.StateOK, Facts: map[string]string{}}
	sv := r.Sysvol
	switch {
	case sv.State < 0 && r.Services["NtFrs"].Status == "Running":
		ch.State = model.StateWarn
		ch.Message = "SYSVOL wird noch per FRS repliziert – auf DFSR migrieren (dfsrmig), neuere Windows-Versionen können sonst nicht beitreten"
		ch.Facts["Replikation"] = "FRS"
	case sv.State < 0:
		ch.State = model.StateUnknown
		ch.Message = "DFSR-Zustand von SYSVOL nicht lesbar"
	default:
		ch.Facts["Replikation"] = "DFSR"
		ch.Facts["DFSR-Zustand"] = cmp.Or(dfsrState[sv.State], strconv.Itoa(sv.State))
		switch sv.State {
		case 4:
			ch.Message = "DFSR-Replikation normal"
		case 5:
			ch.State = model.StateCrit
			ch.Message = "DFSR-Replikation von SYSVOL im Fehlerzustand – Gruppenrichtlinien werden nicht verteilt"
		default:
			ch.State = model.StateWarn
			ch.Message = "DFSR-Replikation von SYSVOL: " + cmp.Or(dfsrState[sv.State], strconv.Itoa(sv.State))
		}
	}
	var missing []string
	for _, s := range []string{"SYSVOL", "NETLOGON"} {
		if !slices.ContainsFunc(sv.Shares, func(x string) bool { return strings.EqualFold(x, s) }) {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 {
		ch.State = model.StateCrit
		ch.Message = "Freigabe " + strings.Join(missing, " und ") + " fehlt – Anmeldeskripte und Gruppenrichtlinien erreichen die Clients nicht · " + ch.Message
	} else {
		ch.Message += " · Freigaben SYSVOL und NETLOGON vorhanden"
	}
	return ch
}

// adServices must run on a domain controller; ADWS (needed by the PowerShell module and the
// admin tools) only warns.
var adServices = []struct {
	name, label string
	critical    bool
}{
	{"NTDS", "Active Directory-Domänendienste", true},
	{"Netlogon", "Netlogon", true},
	{"Kdc", "Kerberos-Schlüsselverteilungscenter", true},
	{"DNS", "DNS-Server", true},
	{"DFSR", "DFS-Replikation", true},
	{"W32Time", "Windows-Zeitgeber", true},
	{"ADWS", "Active Directory-Webdienste", false},
}

var serviceStatus = map[string]string{"Running": "läuft", "Stopped": "beendet", "StartPending": "startet", "StopPending": "wird beendet", "": "nicht installiert"}

// serviceCheck is "ad:services": the services of a domain controller (DNS only when installed).
func serviceCheck(r Report) model.Check {
	ch := model.Check{Key: KeyServices, Name: "AD-Dienste", Group: group, State: model.StateOK, Facts: map[string]string{}}
	var bad []string
	n := 0
	for _, s := range adServices {
		svc, ok := r.Services[s.name]
		if !ok && (s.name == "DNS" || s.name == "DFSR" && r.Sysvol.State < 0) {
			continue // DNS elsewhere; FRS domains have no DFSR SYSVOL
		}
		n++
		label := cmp.Or(svc.Display, s.label)
		status := cmp.Or(serviceStatus[svc.Status], svc.Status)
		ch.Facts[label] = status
		if svc.Status != "Running" {
			st := model.StateWarn
			if s.critical {
				st = model.StateCrit
			}
			ch.State = model.Worst(ch.State, st)
			bad = append(bad, label+" "+status)
		}
	}
	if len(bad) > 0 {
		ch.Message = strings.Join(bad, " · ")
	} else {
		ch.Message = fmt.Sprintf("alle %d Dienste laufen", n)
	}
	return ch
}

// timeCheck is "ad:time": the offset to the PDC emulator (ad.time_offset, absolute seconds;
// Kerberos fails beyond 5 minutes) or, on the PDC emulator itself, whether it has a real time
// source.
func timeCheck(r Report) model.Check {
	ch := model.Check{Key: KeyTime, Name: "Zeit", Group: group, State: model.StateOK, Facts: map[string]string{}}
	src := strings.TrimSpace(r.Time.Source)
	if src != "" {
		ch.Facts["Zeitquelle"] = src
	}
	local := strings.Contains(src, "Local CMOS Clock") || strings.Contains(src, "Free-running System Clock")
	switch {
	case !r.Time.Checked && r.PDC != "" && strings.EqualFold(r.PDC, r.Hostname):
		ch.Message = "PDC-Emulator · Zeitquelle " + cmp.Or(src, "unbekannt")
		if local {
			ch.State = model.StateWarn
			ch.Message = "Der PDC-Emulator nimmt die Zeit von seiner eigenen Uhr – eine externe Zeitquelle (NTP) einrichten: w32tm /config /manualpeerlist:… /syncfromflags:manual /reliable:yes /update"
		}
	case !r.Time.Checked:
		ch.Message = "Zeitquelle " + cmp.Or(src, "unbekannt")
	case r.Time.Offset == nil:
		ch.State = model.StateUnknown
		ch.Message = "PDC-Emulator " + shortHost(r.PDC) + " antwortet nicht auf die Zeitabfrage"
	default:
		off := *r.Time.Offset
		ch.Metrics = []model.Metric{{Name: "ad.time_offset", Value: round2(math.Abs(off)), Unit: "s"}}
		sign := "+"
		if off < 0 {
			sign = "−"
		}
		ch.Message = fmt.Sprintf("Abweichung zum PDC-Emulator %s: %s%s s", shortHost(r.PDC), sign, strings.Replace(strconv.FormatFloat(math.Abs(off), 'f', 2, 64), ".", ",", 1))
		if src != "" {
			ch.Message += " · Quelle " + src
		}
	}
	return ch
}
