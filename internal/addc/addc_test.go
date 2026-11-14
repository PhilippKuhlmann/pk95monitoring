package addc

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"pk95monitoring/internal/model"
)

const sample = `{"available":true,"host":"DC01","errors":[],"domain":"corp.local","forest":"corp.local","site":"Zentrale","gc":true,"rodc":false,
 "hostname":"DC01.corp.local","domain_mode":"Windows2016Domain","forest_mode":"Windows2016Forest",
 "pdc":"DC01.corp.local","rid":"DC01.corp.local","infra":"DC02.corp.local","schema":"DC01.corp.local","naming":"DC01.corp.local",
 "replication":[
  {"partner":"CN=NTDS Settings,CN=DC02,CN=Servers,CN=Zentrale,CN=Sites,CN=Configuration,DC=corp,DC=local","partition":"DC=corp,DC=local","last_success":%OK%,"last_attempt":%NOW%,"result":0,"failures":0},
  {"partner":"CN=NTDS Settings,CN=DC02,CN=Servers,CN=Zentrale,CN=Sites,CN=Configuration,DC=corp,DC=local","partition":"CN=Configuration,DC=corp,DC=local","last_success":%OK%,"last_attempt":%NOW%,"result":0,"failures":0},
  {"partner":"CN=NTDS Settings,CN=DC03,CN=Servers,CN=Filiale,CN=Sites,CN=Configuration,DC=corp,DC=local","partition":"DC=corp,DC=local","last_success":%OLD%,"last_attempt":%NOW%,"result":1722,"failures":4},
  {"partner":"CN=NTDS Settings,CN=DC03,CN=Servers,CN=Filiale,CN=Sites,CN=Configuration,DC=corp,DC=local","partition":"DC=DomainDnsZones,DC=corp,DC=local","last_success":%OK%,"last_attempt":%NOW%,"result":0,"failures":0}
 ],
 "services":{"NTDS":{"status":"Running","start":"Automatic","display":"Active Directory-Domänendienste"},"Netlogon":{"status":"Running"},
  "Kdc":{"status":"Running"},"DNS":{"status":"Running"},"DFSR":{"status":"Running"},"W32Time":{"status":"Running"},"ADWS":{"status":"Stopped","display":"Active Directory-Webdienste"}},
 "sysvol":{"state":4,"migration":3,"shares":["NETLOGON","SYSVOL"]},
 "time":{"source":"ptbtime1.ptb.de","checked":false}}`

func byKey(cs []model.Check) map[string]model.Check {
	m := map[string]model.Check{}
	for _, c := range cs {
		m[c.Key] = c
	}
	return m
}

func TestChecks(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	unix := func(d time.Duration) string { return strconv.FormatInt(now.Add(-d).Unix(), 10) }
	r, err := Parse(strings.NewReplacer("%OK%", unix(10*time.Minute), "%OLD%", unix(3*time.Hour), "%NOW%", unix(time.Minute)).Replace(sample))
	if err != nil || !r.Available || len(r.Replication) != 4 || len(r.Sysvol.Shares) != 2 {
		t.Fatalf("parse %+v %v", r, err)
	}
	m := byKey(Checks(r, nil, now))
	dc := m["ad:dc"]
	if dc.State != model.StateOK || dc.Message != "corp.local · Standort Zentrale · Globaler Katalog · FSMO: PDC-Emulator, RID-Master, Schemamaster, Domänennamenmaster" ||
		dc.Facts["PDC-Emulator"] != "DC01" {
		t.Errorf("dc %+v", dc)
	}
	dc02 := m["ad:repl:dc02"]
	if dc02.State != model.StateOK || dc02.Name != "Replikation von DC02" || !strings.HasPrefix(dc02.Message, "2 Partitionen in Ordnung · letzte erfolgreiche Replikation vor 10 Min.") ||
		dc02.Facts["Partitionen"] != "Konfiguration, corp.local" || dc02.Metrics[0].Name != "ad.repl_age" {
		t.Errorf("dc02 %+v", dc02)
	}
	dc03 := m["ad:repl:dc03"]
	if dc03.State != model.StateWarn || !strings.HasPrefix(dc03.Message, "4 Versuche fehlgeschlagen (corp.local) · Fehler 1722: RPC-Server") ||
		!strings.HasSuffix(dc03.Message, "vor 3 Std.") || dc03.Metrics[0].Value != 3 {
		t.Errorf("dc03 %+v", dc03)
	}
	if sv := m["ad:sysvol"]; sv.State != model.StateOK || sv.Message != "DFSR-Replikation normal · Freigaben SYSVOL und NETLOGON vorhanden" {
		t.Errorf("sysvol %+v", sv)
	}
	if s := m["ad:services"]; s.State != model.StateWarn || s.Message != "Active Directory-Webdienste beendet" {
		t.Errorf("services %+v", s)
	}
	if tm := m["ad:time"]; tm.State != model.StateOK || tm.Message != "PDC-Emulator · Zeitquelle ptbtime1.ptb.de" {
		t.Errorf("time %+v", tm)
	}
}

func TestProblems(t *testing.T) {
	now := time.Now()
	off := -312.5
	r := Report{Available: true, Domain: "corp.local", Hostname: "DC02.corp.local", PDC: "DC01.corp.local",
		Replication: []Link{{Partner: `CN=NTDS Settings\0ADEL:1234,CN=OLDDC,CN=Servers`, Partition: "CN=Schema,CN=Configuration,DC=corp,DC=local", Result: 8614, Failures: 90}},
		Services:    map[string]Service{"NTDS": {Status: "Running"}, "Netlogon": {Status: "Stopped"}, "Kdc": {Status: "Running"}, "W32Time": {Status: "Running"}, "ADWS": {Status: "Running"}, "DFSR": {Status: "Running"}},
		Sysvol:      Sysvol{State: 5, Shares: []string{"SYSVOL"}},
		Time:        Time{Checked: true, Offset: &off, Source: "DC01.corp.local"},
	}
	m := byKey(Checks(r, nil, now))
	old := m["ad:repl:olddc"]
	if old.State != model.StateCrit || old.Name != "Replikation von OLDDC (gelöscht)" || !strings.Contains(old.Message, "Tombstone") || !strings.Contains(old.Message, "noch nie") {
		t.Errorf("tombstone %+v", old)
	}
	if sv := m["ad:sysvol"]; sv.State != model.StateCrit || !strings.HasPrefix(sv.Message, "Freigabe NETLOGON fehlt") || !strings.Contains(sv.Message, "Fehlerzustand") {
		t.Errorf("sysvol %+v", sv)
	}
	if s := m["ad:services"]; s.State != model.StateCrit || s.Message != "Netlogon beendet" {
		t.Errorf("services %+v", s)
	}
	if _, ok := m["ad:services"].Facts["DNS-Server"]; ok {
		t.Error("DNS listed although it is not installed")
	}
	if tm := m["ad:time"]; tm.Metrics[0].Value != 312.5 || tm.Message != "Abweichung zum PDC-Emulator DC01: −312,50 s · Quelle DC01.corp.local" {
		t.Errorf("time %+v", tm)
	}
}

func TestVariants(t *testing.T) {
	if r, err := Parse(`{"available":false}`); err != nil || Checks(r, nil, time.Now()) != nil {
		t.Errorf("no DC: %+v %v", r, err)
	}
	// single entries arrive as objects; the AD module failed
	r, err := Parse(`{"available":true,"errors":"Das Modul ActiveDirectory fehlt","sysvol":{"state":-1,"migration":-1,"shares":"SYSVOL"},
		"services":{"NtFrs":{"status":"Running"},"NTDS":{"status":"Running"}},"time":{"source":"Local CMOS Clock"}}`)
	if err != nil || len(r.Errors) != 1 || len(r.Sysvol.Shares) != 1 {
		t.Fatalf("variants %+v %v", r, err)
	}
	m := byKey(Checks(r, nil, time.Now()))
	if dc := m["ad:dc"]; dc.State != model.StateUnknown || !strings.Contains(dc.Message, "Modul ActiveDirectory") {
		t.Errorf("dc %+v", dc)
	}
	if sv := m["ad:sysvol"]; sv.State != model.StateCrit || !strings.Contains(sv.Message, "FRS") {
		t.Errorf("frs %+v", sv)
	}
	if len(m) != 4 {
		t.Errorf("without the AD module there are no replication checks: %v", m)
	}
	single, _ := Parse(`{"available":true,"domain":"a.b","hostname":"DC1.a.b","pdc":"DC1.a.b","replication":null,"sysvol":{"state":4,"shares":["SYSVOL","NETLOGON"]},
		"services":{},"time":{"source":"Local CMOS Clock","checked":false}}`)
	m = byKey(Checks(single, nil, time.Now()))
	if rp := m["ad:repl:-"]; rp.State != model.StateOK || !strings.Contains(rp.Message, "einziger") {
		t.Errorf("single DC %+v", rp)
	}
	if tm := m["ad:time"]; tm.State != model.StateWarn || !strings.Contains(tm.Message, "eigenen Uhr") {
		t.Errorf("PDC with local clock %+v", tm)
	}
	if cs := Checks(Report{}, errors.New("Zeitüberschreitung"), time.Now()); len(cs) != 1 || cs[0].State != model.StateUnknown {
		t.Errorf("error %+v", cs)
	}
}
