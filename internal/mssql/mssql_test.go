package mssql

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"pk95monitoring/internal/model"
)

// A default instance with agent and a named Express instance; single entries come as objects.
const sample = `{"available":true,"instances":[
 {"name":"MSSQLSERVER","service":{"status":"Running","start":"Automatic"},"agent":{"status":"Running","start":"Automatic"},
  "version":"15.0.4375.4","edition":"Standard Edition (64-bit)","level":"RTM","update":"CU25","engine":2,
  "databases":[
   {"name":"ERP","system":false,"state":"ONLINE","recovery":"FULL","data_mb":43520,"log_mb":8192,"log_used":12.5,"log_wait":"NOTHING","last_full":%DAY%,"last_diff":%H6%,"last_log":%M15%},
   {"name":"Archiv","system":false,"state":"ONLINE","recovery":"FULL","read_only":false,"data_mb":2048,"log_mb":30720,"log_used":97.1,"log_wait":"LOG_BACKUP","last_full":%DAY%,"last_diff":0,"last_log":0},
   {"name":"Test","system":false,"state":"RECOVERY_PENDING","recovery":"SIMPLE","data_mb":100,"log_mb":10,"log_used":-1,"last_full":0,"last_diff":0,"last_log":0},
   {"name":"master","system":true,"state":"ONLINE","recovery":"SIMPLE","data_mb":6,"log_mb":2,"log_used":40,"last_full":0,"last_diff":0,"last_log":0},
   {"name":"model","system":true,"state":"ONLINE","recovery":"FULL","data_mb":16,"log_mb":8,"log_used":10,"last_full":0,"last_diff":0,"last_log":0}],
  "jobs":[{"name":"Sicherung ERP","enabled":true,"status":0,"run":%H6%,"message":"Der Auftrag ist fehlgeschlagen.\r\nSchritt 1"},
   {"name":"Indexpflege","enabled":true,"status":1,"run":%DAY%,"message":""},
   {"name":"Alt","enabled":false,"status":0,"run":0,"message":""},
   {"name":"Neu","enabled":true,"status":-1,"run":0,"message":""}]},
 {"name":"SQLEXPRESS","service":{"status":"Running","start":"Automatic"},
  "version":"13.0.7029.3","edition":"Express Edition (64-bit)","level":"SP3","update":"","engine":4,
  "databases":{"name":"Zeiterfassung","system":false,"state":"ONLINE","recovery":"SIMPLE","data_mb":9216,"log_mb":512,"log_used":5,"last_full":%DAY%,"last_diff":0,"last_log":0},
  "jobs":[]}]}`

func byKey(cs []model.Check) map[string]model.Check {
	m := map[string]model.Check{}
	for _, c := range cs {
		m[c.Key] = c
	}
	return m
}

func metric(c model.Check, name string) (float64, bool) {
	for _, m := range c.Metrics {
		if m.Name == name {
			return m.Value, true
		}
	}
	return 0, false
}

func TestChecks(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) string { return strconv.FormatInt(now.Add(-d).Unix(), 10) }
	r, err := Parse("Warnung\r\n" + strings.NewReplacer("%DAY%", ago(26*time.Hour), "%H6%", ago(6*time.Hour), "%M15%", ago(15*time.Minute)).Replace(sample))
	if err != nil || !r.Available || len(r.Instances) != 2 || len(r.Instances[1].Databases) != 1 {
		t.Fatalf("parse %+v %v", r, err)
	}
	m := byKey(Checks(r, nil, now))
	if s := m["mssql:mssqlserver"]; s.State != model.StateOK || s.Name != "SQL Server" || s.Message != "SQL Server 2019 Standard · CU25 · 3 Datenbanken" ||
		s.Facts["Support bis"] != "08.01.2030" {
		t.Errorf("instance %+v", s)
	}
	if s := m["mssql:sqlexpress"]; s.State != model.StateWarn || s.Name != "SQL Server SQLEXPRESS" ||
		s.Message != "Kein Support mehr seit 07/2026 · SQL Server 2016 Express · SP3 · 1 Datenbank" {
		t.Errorf("express instance %+v", s)
	}
	erp := m["mssql:db:mssqlserver/ERP"]
	if erp.State != model.StateOK || erp.Message != "42,5 GiB · Differenzsicherung vor 6 Std." || erp.Facts["Wiederherstellungsmodell"] != "Vollständig" {
		t.Errorf("ERP %+v", erp)
	}
	if v, _ := metric(erp, "backup.age"); v != 6 {
		t.Errorf("backup age %v", v)
	}
	if v, _ := metric(erp, "mssql.log_backup_age"); v != 0.3 {
		t.Errorf("log backup age %v", v)
	}
	if a := m["mssql:db:mssqlserver/Archiv"]; a.State != model.StateWarn || !strings.Contains(a.Message, "ohne Protokollsicherung") || a.Facts["Log wartet auf"] != "LOG_BACKUP" {
		t.Errorf("full without log backup %+v", a)
	}
	if tst := m["mssql:db:mssqlserver/Test"]; tst.State != model.StateCrit || !strings.HasPrefix(tst.Message, "Datenbank Wiederherstellung ausstehend") {
		t.Errorf("recovery pending %+v", tst)
	}
	if ms := m["mssql:db:mssqlserver/master"]; ms.State != model.StateOK || ms.Facts["Art"] != "Systemdatenbank" {
		t.Errorf("master without backup is no warning %+v", ms)
	}
	if md := m["mssql:db:mssqlserver/model"]; md.State != model.StateOK || strings.Contains(md.Message, "Protokoll") {
		t.Errorf("model %+v", md)
	}
	z := m["mssql:db:sqlexpress/Zeiterfassung"]
	if v, _ := metric(z, "mssql.express_usage"); v != 90 || z.Name != "Zeiterfassung (SQLEXPRESS)" || !strings.Contains(z.Message, "9,0 GiB von 10 GiB (90 %)") {
		t.Errorf("express database %+v", z)
	}
	if j := m["mssql:job:mssqlserver/Sicherung ERP"]; j.State != model.StateCrit || j.Message != "Fehlgeschlagen vor 6 Std." || j.Facts["Meldung"] != "Der Auftrag ist fehlgeschlagen." {
		t.Errorf("failed job %+v", j)
	}
	if j := m["mssql:job:mssqlserver/Neu"]; j.State != model.StateOK || j.Message != "Noch nie gelaufen" {
		t.Errorf("new job %+v", j)
	}
	if _, ok := m["mssql:job:mssqlserver/Alt"]; ok {
		t.Error("disabled job listed")
	}
}

func TestProblems(t *testing.T) {
	now := time.Now()
	r := Report{Available: true, Instances: []Instance{
		{Name: "MSSQLSERVER", Service: &Service{Status: "Stopped", Start: "Automatic"}, Agent: &Service{Status: "Stopped", Start: "Automatic"}},
		{Name: "SAGE", Service: &Service{Status: "Running", Start: "Automatic"}, Error: "Login failed for user 'NT AUTHORITY\\SYSTEM'."},
		{Name: "UNUSED", Service: &Service{Status: "Stopped", Start: "Manual"}},
		{Name: "ERP", Service: &Service{Status: "Running", Start: "Automatic"}, Agent: &Service{Status: "Stopped", Start: "Automatic"},
			Version: "11.0.7001.0", Edition: "Standard Edition", Engine: 2, JobsError: "SELECT permission was denied"},
	}}
	m := byKey(Checks(r, nil, now))
	if s := m["mssql:mssqlserver"]; s.State != model.StateCrit || len(m) != 5 {
		t.Errorf("stopped %+v (%d checks)", s, len(m))
	}
	if s := m["mssql:sage"]; s.State != model.StateWarn || !strings.Contains(s.Message, "Keine Leserechte") || !strings.Contains(s.Facts["T-SQL"], "SQLAgentReaderRole") {
		t.Errorf("login %+v", s)
	}
	if s := m["mssql:unused"]; s.State != model.StateOK || s.Message != "Dienst beendet (Starttyp manuell)" {
		t.Errorf("manual %+v", s)
	}
	if s := m["mssql:erp"]; s.State != model.StateWarn || !strings.HasPrefix(s.Message, "SQL-Server-Agent beendet") || !strings.Contains(s.Message, "Kein Support mehr seit 07/2022") {
		t.Errorf("agent stopped %+v", s)
	}
	if j := m["mssql:job:erp"]; j.State != model.StateWarn {
		t.Errorf("jobs not readable %+v", j)
	}
	if cs := Checks(Report{}, nil, now); cs != nil {
		t.Error("no SQL Server")
	}
	if cs := Checks(Report{}, errors.New("timeout"), now); len(cs) != 1 || cs[0].State != model.StateUnknown {
		t.Errorf("error %+v", cs)
	}
	if r, err := Parse(`{"available":false}`); err != nil || r.Available {
		t.Errorf("not installed %+v %v", r, err)
	}
}

func TestRelease(t *testing.T) {
	for v, want := range map[string]string{"16.0.4135.4": "2022", "10.50.6000.34": "2008 R2", "10.0.6000.29": "2008", "99.0.1": "", "x": ""} {
		if got, _ := release(v); got != want {
			t.Errorf("%s: %q, want %q", v, got, want)
		}
	}
}
