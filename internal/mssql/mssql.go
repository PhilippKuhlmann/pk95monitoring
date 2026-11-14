// Package mssql turns the state of the Microsoft SQL Server instances of a Windows server into
// checks: each instance (service, version, support end, SQL Server Agent), each database
// (state, last backup, log backups in the full recovery model, size, the 10 GB limit of
// Express) and each SQL Server Agent job (last run). The agent runs Script with PowerShell
// and hands the output to Parse; the demo builds a Report directly. The package is platform
// independent.
//
// The agent runs as LocalSystem and logs in with Windows authentication. Since SQL Server
// 2012 that account has no rights in SQL Server; Grant lists the read-only rights it needs.
package mssql

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"pk95monitoring/internal/model"
)

// Check keys and groups.
const (
	KeyInstance = "mssql:"     // + instance (lower case)
	KeyDB       = "mssql:db:"  // + instance/database
	KeyJob      = "mssql:job:" // + instance/job
	groupServer = "SQL Server"
	groupDB     = "SQL-Datenbanken"
	groupJobs   = "SQL-Agent-Aufträge"

	expressLimitMB = 10240 // data size limit per database of SQL Server Express
	maxDatabases   = 200
	maxJobs        = 200
)

// Grant is the T-SQL that gives the agent (LocalSystem) the read-only rights it needs.
const Grant = `USE master;
IF SUSER_ID(N'NT AUTHORITY\SYSTEM') IS NULL CREATE LOGIN [NT AUTHORITY\SYSTEM] FROM WINDOWS;
GRANT VIEW SERVER STATE TO [NT AUTHORITY\SYSTEM];
GRANT VIEW ANY DEFINITION TO [NT AUTHORITY\SYSTEM];
USE msdb;
IF USER_ID(N'NT AUTHORITY\SYSTEM') IS NULL CREATE USER [NT AUTHORITY\SYSTEM] FOR LOGIN [NT AUTHORITY\SYSTEM];
ALTER ROLE db_datareader ADD MEMBER [NT AUTHORITY\SYSTEM];
ALTER ROLE SQLAgentReaderRole ADD MEMBER [NT AUTHORITY\SYSTEM];`

// Script lists the SQL Server instances of the server as JSON; without SQL Server it prints
// {"available":false}. It connects locally with Windows authentication (System.Data.SqlClient
// of the .NET Framework, no module needed). Backup times come from msdb (local server time),
// the log usage from DBCC SQLPERF(LOGSPACE), the jobs from msdb.dbo.sysjobhistory.
const Script = `$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [Text.Encoding]::UTF8
$names = @()
try {
  $p = Get-ItemProperty -Path 'HKLM:\SOFTWARE\Microsoft\Microsoft SQL Server\Instance Names\SQL' -ErrorAction Stop
  $names = @($p.PSObject.Properties | Where-Object { $_.Name -notlike 'PS*' } | ForEach-Object { $_.Name })
} catch {}
if ($names.Count -eq 0) { Write-Output '{"available":false}'; exit 0 }
$epoch = [datetime]'1970-01-01'
$ts = { param($d) if ($d -is [datetime] -and $d.Year -gt 1970) { [int64](($d.ToUniversalTime() - $epoch).TotalSeconds) } else { 0 } }
$query = { param($conn, $sql)
  $cmd = $conn.CreateCommand(); $cmd.CommandText = $sql; $cmd.CommandTimeout = 30
  $t = New-Object System.Data.DataTable; $t.Load($cmd.ExecuteReader()); ,$t }
$out = @{ available = $true; instances = @() }
foreach ($n in $names) {
  $def = $n -eq 'MSSQLSERVER'
  $i = @{ name = $n; databases = @(); jobs = @() }
  $s = Get-Service -Name $(if ($def) { 'MSSQLSERVER' } else { 'MSSQL$' + $n }) -ErrorAction SilentlyContinue
  if ($s) { $i.service = @{ status = "$($s.Status)"; start = "$($s.StartType)" } }
  $a = Get-Service -Name $(if ($def) { 'SQLSERVERAGENT' } else { 'SQLAgent$' + $n }) -ErrorAction SilentlyContinue
  if ($a) { $i.agent = @{ status = "$($a.Status)"; start = "$($a.StartType)" } }
  if (-not $s -or $s.Status -ne 'Running') { $out.instances += $i; continue }
  $ds = if ($def) { '.' } else { ".\$n" }
  $conn = New-Object System.Data.SqlClient.SqlConnection "Data Source=$ds;Integrated Security=SSPI;Connect Timeout=10;Application Name=PK95 Agent"
  try {
    $conn.Open()
    $v = (& $query $conn "SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)) v, CAST(SERVERPROPERTY('Edition') AS nvarchar(128)) e, CAST(SERVERPROPERTY('ProductLevel') AS nvarchar(32)) l, CAST(ISNULL(SERVERPROPERTY('ProductUpdateLevel'), '') AS nvarchar(32)) u, CAST(SERVERPROPERTY('EngineEdition') AS int) g").Rows[0]
    $i.version = "$($v.v)"; $i.edition = "$($v.e)"; $i.level = "$($v.l)"; $i.update = "$($v.u)"; $i.engine = [int]$v.g
    $log = @{}
    try { foreach ($r in (& $query $conn 'DBCC SQLPERF(LOGSPACE) WITH NO_INFOMSGS').Rows) { $log["$($r[0])"] = [double]$r[2] } } catch {}
    $sql = "SELECT d.name, d.database_id id, d.state_desc st, d.recovery_model_desc rm, d.is_read_only ro, d.log_reuse_wait_desc lw,
 CAST(ISNULL((SELECT SUM(CAST(size AS bigint)) FROM sys.master_files f WHERE f.database_id = d.database_id AND f.type = 0), 0) * 8 / 1024.0 AS float) dm,
 CAST(ISNULL((SELECT SUM(CAST(size AS bigint)) FROM sys.master_files f WHERE f.database_id = d.database_id AND f.type = 1), 0) * 8 / 1024.0 AS float) lm,
 (SELECT MAX(backup_finish_date) FROM msdb.dbo.backupset b WHERE b.database_name = d.name AND b.type = 'D') bf,
 (SELECT MAX(backup_finish_date) FROM msdb.dbo.backupset b WHERE b.database_name = d.name AND b.type = 'I') bi,
 (SELECT MAX(backup_finish_date) FROM msdb.dbo.backupset b WHERE b.database_name = d.name AND b.type = 'L') bl
FROM sys.databases d WHERE d.database_id <> 2 AND d.source_database_id IS NULL"
    foreach ($r in (& $query $conn $sql).Rows) {
      $lu = -1; if ($log.ContainsKey("$($r.name)")) { $lu = $log["$($r.name)"] }
      $i.databases += @{ name = "$($r.name)"; system = ([int]$r.id -le 4); state = "$($r.st)"; recovery = "$($r.rm)"; read_only = [bool]$r.ro;
        log_wait = "$($r.lw)"; data_mb = [double]$r.dm; log_mb = [double]$r.lm; log_used = $lu
        last_full = (& $ts $r.bf); last_diff = (& $ts $r.bi); last_log = (& $ts $r.bl) }
    }
    if ($a) {
      try {
        $jq = "SELECT j.name, j.enabled en, ISNULL(h.run_status, -1) rs, CASE WHEN h.run_date > 0 THEN msdb.dbo.agent_datetime(h.run_date, h.run_time) END rt, ISNULL(h.message, '') msg
 FROM msdb.dbo.sysjobs j OUTER APPLY (SELECT TOP 1 x.run_status, x.run_date, x.run_time, x.message FROM msdb.dbo.sysjobhistory x WHERE x.job_id = j.job_id AND x.step_id = 0 ORDER BY x.instance_id DESC) h"
        foreach ($r in (& $query $conn $jq).Rows) {
          $i.jobs += @{ name = "$($r.name)"; enabled = [bool]$r.en; status = [int]$r.rs; run = (& $ts $r.rt); message = "$($r.msg)" }
        }
      } catch { $i.jobs_error = "$($_.Exception.Message)" }
    }
  } catch { $i.error = "$($_.Exception.Message)" } finally { $conn.Dispose() }
  $out.instances += $i
}
Write-Output ($out | ConvertTo-Json -Depth 6 -Compress)
`

// list decodes a JSON list or a single element (ConvertTo-Json unwraps one-element lists).
type list[T any] []T

func (l *list[T]) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	switch {
	case s == "null" || s == "":
		*l = nil
		return nil
	case strings.HasPrefix(s, "["):
		var v []T
		err := json.Unmarshal(b, &v)
		*l = v
		return err
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*l = []T{v}
	return nil
}

// Report is the parsed output of Script.
type Report struct {
	Available bool           `json:"available"`
	Instances list[Instance] `json:"instances"`
}

// Instance is one SQL Server instance.
type Instance struct {
	Name      string         `json:"name"` // MSSQLSERVER = default instance
	Service   *Service       `json:"service"`
	Agent     *Service       `json:"agent"` // nil: no SQL Server Agent (Express)
	Error     string         `json:"error"` // connection or query error
	Version   string         `json:"version"`
	Edition   string         `json:"edition"`
	Level     string         `json:"level"`  // RTM, SP1 …
	Update    string         `json:"update"` // CU25 …
	Engine    int            `json:"engine"` // EngineEdition: 4 = Express
	Databases list[Database] `json:"databases"`
	Jobs      list[Job]      `json:"jobs"`
	JobsError string         `json:"jobs_error"`
}

// Service is a Windows service.
type Service struct {
	Status string `json:"status"` // Running, Stopped …
	Start  string `json:"start"`  // Automatic, Manual, Disabled
}

// Database is one database (tempdb and snapshots left out).
type Database struct {
	Name     string  `json:"name"`
	System   bool    `json:"system"`   // master, model, msdb
	State    string  `json:"state"`    // ONLINE, OFFLINE, RESTORING, RECOVERING, RECOVERY_PENDING, SUSPECT, EMERGENCY
	Recovery string  `json:"recovery"` // SIMPLE, FULL, BULK_LOGGED
	ReadOnly bool    `json:"read_only"`
	LogWait  string  `json:"log_wait"` // log_reuse_wait_desc
	DataMB   float64 `json:"data_mb"`
	LogMB    float64 `json:"log_mb"`
	LogUsed  float64 `json:"log_used"` // percent of the log in use, -1 = unknown
	LastFull int64   `json:"last_full"`
	LastDiff int64   `json:"last_diff"`
	LastLog  int64   `json:"last_log"`
}

// Job is a SQL Server Agent job with its last run.
type Job struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Status  int    `json:"status"` // 0 failed, 1 succeeded, 2 retry, 3 canceled, 4 running, -1 never ran
	Run     int64  `json:"run"`
	Message string `json:"message"`
}

// Parse decodes the output of Script.
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
	var r Report
	err := json.Unmarshal([]byte(s), &r)
	return r, err
}

// releases are the SQL Server versions by major version with the end of Microsoft's extended
// support, newest first.
var releases = []struct {
	major, minor int
	name, end    string
}{
	{17, 0, "2025", "2036-01-06"},
	{16, 0, "2022", "2033-01-11"},
	{15, 0, "2019", "2030-01-08"},
	{14, 0, "2017", "2027-10-12"},
	{13, 0, "2016", "2026-07-14"},
	{12, 0, "2014", "2024-07-09"},
	{11, 0, "2012", "2022-07-12"},
	{10, 50, "2008 R2", "2019-07-09"},
	{10, 0, "2008", "2019-07-09"},
}

// release returns the product year and the support end of a ProductVersion ("15.0.4375.4").
func release(version string) (string, time.Time) {
	parts := strings.Split(version, ".")
	major, err := strconv.Atoi(parts[0])
	if err != nil || major > releases[0].major {
		return "", time.Time{}
	}
	minor := 0
	if len(parts) > 1 {
		minor, _ = strconv.Atoi(parts[1])
	}
	for _, r := range releases {
		if major > r.major || (major == r.major && minor >= r.minor) {
			end, _ := time.Parse("2006-01-02", r.end)
			return r.name, end
		}
	}
	return "", time.Time{}
}

func instanceKey(name string) string { return strings.ToLower(name) }

// label is how an instance is named in check names: "" for the default instance.
func label(name string) string {
	if name == "MSSQLSERVER" {
		return ""
	}
	return name
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func fmtAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "gerade eben"
	case d < time.Hour:
		return fmt.Sprintf("vor %d Min.", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("vor %d Std.", int(d.Hours()))
	}
	return fmt.Sprintf("vor %d Tagen", int(d.Hours()/24))
}

func fmtMB(mb float64) string {
	switch {
	case mb >= 1024*1024:
		return strings.Replace(fmt.Sprintf("%.1f TiB", mb/1024/1024), ".", ",", 1)
	case mb >= 1024:
		return strings.Replace(fmt.Sprintf("%.1f GiB", mb/1024), ".", ",", 1)
	}
	return fmt.Sprintf("%.0f MiB", mb)
}

func fmtTS(ts int64) string { return time.Unix(ts, 0).Local().Format("02.01.2006 15:04") }

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func worse(a, b string) string {
	if model.Rank(b) > model.Rank(a) {
		return b
	}
	return a
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(s)
}

// Checks turns a report into checks; err is a failure of Script itself.
func Checks(r Report, err error, now time.Time) []model.Check {
	if err != nil {
		return []model.Check{{Key: "mssql", Name: "SQL Server", Group: groupServer, State: model.StateUnknown,
			Message: "Abfrage fehlgeschlagen: " + err.Error()}}
	}
	if !r.Available {
		return nil
	}
	var out []model.Check
	for _, in := range r.Instances {
		out = append(out, instanceCheck(in, now))
		if in.Error != "" || in.Service == nil || in.Service.Status != "Running" {
			continue
		}
		dbs := slices.Clone(in.Databases)
		slices.SortFunc(dbs, func(a, b Database) int {
			if a.System != b.System {
				if a.System {
					return 1
				}
				return -1
			}
			return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		})
		for i, db := range dbs {
			if i == maxDatabases {
				break
			}
			out = append(out, dbCheck(in, db, in.Engine == 4, now))
		}
		out = append(out, jobChecks(in, now)...)
	}
	return out
}

var serviceStatus = map[string]string{"Running": "läuft", "Stopped": "beendet", "StartPending": "startet", "StopPending": "wird beendet", "Paused": "angehalten"}
var startType = map[string]string{"Automatic": "automatisch", "Manual": "manuell", "Disabled": "deaktiviert"}

func serviceWord(s *Service) string {
	if s == nil {
		return "nicht installiert"
	}
	w := serviceStatus[s.Status]
	if w == "" {
		w = s.Status
	}
	if t := startType[s.Start]; t != "" {
		w += " (" + t + ")"
	}
	return w
}

// loginDenied reports whether a connection error is a missing login or right of the agent.
func loginDenied(e string) bool {
	for _, s := range []string{"Login failed", "Fehler bei der Anmeldung", "NT AUTHORITY", "NT-AUTORITÄT", "permission was denied", "Berechtigung"} {
		if strings.Contains(e, s) {
			return true
		}
	}
	return false
}

func instanceCheck(in Instance, now time.Time) model.Check {
	name := "SQL Server"
	if l := label(in.Name); l != "" {
		name += " " + l
	}
	ch := model.Check{Key: KeyInstance + instanceKey(in.Name), Name: name, Group: groupServer, State: model.StateOK,
		Facts: map[string]string{"Instanz": in.Name, "Dienst": serviceWord(in.Service)}}
	if in.Agent != nil {
		ch.Facts["SQL-Server-Agent"] = serviceWord(in.Agent)
	}
	switch {
	case in.Service == nil:
		ch.State, ch.Message = model.StateUnknown, "Dienst nicht gefunden"
		return ch
	case in.Service.Status != "Running":
		switch in.Service.Start {
		case "Automatic":
			ch.State, ch.Message = model.StateCrit, "Dienst beendet, obwohl er automatisch startet"
		case "Disabled":
			ch.Message = "Dienst deaktiviert"
		default:
			ch.Message = "Dienst beendet (Starttyp manuell)"
		}
		return ch
	case in.Error != "":
		ch.State = model.StateWarn
		ch.Facts["Fehler"] = firstLine(in.Error)
		if loginDenied(in.Error) {
			ch.Message = "Keine Leserechte für den Agent (NT-AUTORITÄT\\SYSTEM) – Rechte einmalig per T-SQL vergeben (siehe Fakten)"
			ch.Facts["T-SQL"] = Grant
		} else {
			ch.Message = "Verbindung fehlgeschlagen: " + firstLine(in.Error)
		}
		return ch
	}
	year, end := release(in.Version)
	product := "SQL Server"
	if year != "" {
		product += " " + year
	}
	edition := strings.TrimSuffix(strings.TrimSuffix(in.Edition, " (64-bit)"), " Edition")
	parts := []string{strings.TrimSpace(product + " " + edition)}
	upd := in.Update
	if upd == "" && in.Level != "" && in.Level != "RTM" {
		upd = in.Level
	}
	if upd != "" {
		parts = append(parts, upd)
	}
	n := 0
	for _, db := range in.Databases {
		if !db.System {
			n++
		}
	}
	parts = append(parts, fmt.Sprintf("%d %s", n, plural(n, "Datenbank", "Datenbanken")))
	ch.Message = strings.Join(parts, " · ")
	ch.Facts["Version"] = in.Version
	ch.Facts["Edition"] = in.Edition
	if upd != "" {
		ch.Facts["Update-Stand"] = upd
	}
	if !end.IsZero() {
		ch.Facts["Support bis"] = end.Format("02.01.2006")
		if now.After(end) {
			ch.State = model.StateWarn
			ch.Message = fmt.Sprintf("Kein Support mehr seit %s · %s", end.Format("01/2006"), ch.Message)
		}
	}
	if a := in.Agent; a != nil && a.Status != "Running" && a.Start == "Automatic" {
		ch.State = model.StateWarn
		ch.Message = "SQL-Server-Agent beendet – geplante Aufträge laufen nicht · " + ch.Message
	}
	return ch
}

var stateWord = map[string]string{"OFFLINE": "offline", "RESTORING": "wird wiederhergestellt", "RECOVERING": "wird gestartet",
	"RECOVERY_PENDING": "Wiederherstellung ausstehend", "SUSPECT": "fehlerverdächtig (SUSPECT)", "EMERGENCY": "im Notfallmodus"}

var recoveryWord = map[string]string{"SIMPLE": "Einfach", "FULL": "Vollständig", "BULK_LOGGED": "Massenprotokolliert"}

func dbCheck(in Instance, db Database, express bool, now time.Time) model.Check {
	name := db.Name
	if l := label(in.Name); l != "" {
		name += " (" + l + ")"
	}
	ch := model.Check{Key: KeyDB + instanceKey(in.Name) + "/" + db.Name, Name: name, Group: groupDB, State: model.StateOK,
		Facts: map[string]string{"Wiederherstellungsmodell": recoveryWord[db.Recovery], "Daten": fmtMB(db.DataMB), "Log": fmtMB(db.LogMB)}}
	if db.System {
		ch.Facts["Art"] = "Systemdatenbank"
	}
	ch.Metrics = []model.Metric{{Name: "mssql.data_size", Value: math.Round(db.DataMB * 1024 * 1024), Unit: "B"},
		{Name: "mssql.log_size", Value: math.Round(db.LogMB * 1024 * 1024), Unit: "B"}}
	if db.LogUsed >= 0 {
		ch.Metrics = append(ch.Metrics, model.Metric{Name: "mssql.log_usage", Value: round1(db.LogUsed), Unit: "%"})
	}
	var parts []string
	switch db.State {
	case "ONLINE":
	case "OFFLINE", "RESTORING", "RECOVERING":
		ch.State = model.StateWarn
		parts = append(parts, "Datenbank "+stateWord[db.State])
	default:
		ch.State = model.StateCrit
		w := stateWord[db.State]
		if w == "" {
			w = db.State
		}
		parts = append(parts, "Datenbank "+w)
	}
	if db.LogWait != "" && db.LogWait != "NOTHING" {
		ch.Facts["Log wartet auf"] = db.LogWait
	}
	size := fmtMB(db.DataMB)
	if express && !db.System {
		pct := db.DataMB / expressLimitMB * 100
		ch.Metrics = append(ch.Metrics, model.Metric{Name: "mssql.express_usage", Value: round1(pct), Unit: "%"})
		size += fmt.Sprintf(" von 10 GiB (%.0f %%)", pct)
	}
	parts = append(parts, size)

	// model is never backed up on purpose; system databases without a backup are no warning
	if db.Name != "model" {
		last, kind := db.LastFull, "Vollsicherung"
		if db.LastDiff > last {
			last, kind = db.LastDiff, "Differenzsicherung"
		}
		if db.LastFull > 0 {
			ch.Facts["Letzte Vollsicherung"] = fmtTS(db.LastFull)
		}
		if db.LastDiff > 0 {
			ch.Facts["Letzte Differenzsicherung"] = fmtTS(db.LastDiff)
		}
		switch {
		case last > 0:
			age := now.Sub(time.Unix(last, 0))
			ch.Metrics = append(ch.Metrics, model.Metric{Name: "backup.age", Value: round1(math.Max(age.Hours(), 0)), Unit: "h"})
			parts = append(parts, kind+" "+fmtAge(age))
		case !db.System && db.State == "ONLINE":
			ch.State = worse(ch.State, model.StateWarn)
			parts = append(parts, "noch nie gesichert")
		}
		if (db.Recovery == "FULL" || db.Recovery == "BULK_LOGGED") && !db.System && !db.ReadOnly && db.State == "ONLINE" {
			if db.LastLog > 0 {
				age := now.Sub(time.Unix(db.LastLog, 0))
				ch.Facts["Letzte Protokollsicherung"] = fmtTS(db.LastLog)
				ch.Metrics = append(ch.Metrics, model.Metric{Name: "mssql.log_backup_age", Value: round1(math.Max(age.Hours(), 0)), Unit: "h"})
			} else {
				ch.State = worse(ch.State, model.StateWarn)
				parts = append(parts, "Modell „Vollständig“ ohne Protokollsicherung – das Log wächst unbegrenzt")
			}
		}
	}
	ch.Message = strings.Join(parts, " · ")
	return ch
}

func jobChecks(in Instance, now time.Time) []model.Check {
	if in.Agent == nil {
		return nil
	}
	if in.JobsError != "" {
		return []model.Check{{Key: KeyJob + instanceKey(in.Name), Name: "SQL-Agent-Aufträge", Group: groupJobs, State: model.StateWarn,
			Message: "Aufträge nicht lesbar – der Agent braucht die Rolle SQLAgentReaderRole in msdb (siehe Fakten)",
			Facts:   map[string]string{"Fehler": firstLine(in.JobsError), "T-SQL": Grant}}}
	}
	jobs := slices.Clone(in.Jobs)
	slices.SortFunc(jobs, func(a, b Job) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	var out []model.Check
	for _, j := range jobs {
		if !j.Enabled {
			continue
		}
		if len(out) == maxJobs {
			break
		}
		name := j.Name
		if l := label(in.Name); l != "" {
			name += " (" + l + ")"
		}
		ch := model.Check{Key: KeyJob + instanceKey(in.Name) + "/" + j.Name, Name: name, Group: groupJobs, State: model.StateOK, Facts: map[string]string{}}
		when := ""
		if j.Run > 0 {
			when = " " + fmtAge(now.Sub(time.Unix(j.Run, 0)))
			ch.Facts["Letzter Lauf"] = fmtTS(j.Run)
		}
		switch j.Status {
		case 0:
			ch.State, ch.Message = model.StateCrit, "Fehlgeschlagen"+when
			if m := firstLine(j.Message); m != "" {
				ch.Facts["Meldung"] = m
			}
		case 1:
			ch.Message = "Erfolgreich" + when
		case 2:
			ch.State, ch.Message = model.StateWarn, "Wird wiederholt"+when
		case 3:
			ch.State, ch.Message = model.StateWarn, "Abgebrochen"+when
		case 4:
			ch.Message = "Läuft"
		default:
			ch.Message = "Noch nie gelaufen"
		}
		out = append(out, ch)
	}
	return out
}
