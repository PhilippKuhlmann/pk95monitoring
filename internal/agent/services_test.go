package agent

import (
	"slices"
	"testing"
)

func pickKeys(ps []svcPick) []string {
	var out []string
	for _, p := range ps {
		k := p.Key
		if !p.Found {
			k += "!"
		}
		if p.Optional {
			k += "?"
		}
		out = append(out, k)
	}
	return out
}

func TestMatchNameGlob(t *testing.T) {
	for _, tc := range []struct {
		p, s string
		want bool
	}{
		{"veeam*", "VeeamBackupSvc", true},
		{"*sql*", "MSSQLSERVER", true},
		{"MSSQL$*", "MSSQL$SQLEXPRESS", true},
		{"veeam*", "MyVeeam", false},
		{"spooler", "Spooler", true},
		{"spooler", "Spooler2", false},
	} {
		if got := matchName(tc.p, tc.s); got != tc.want {
			t.Errorf("matchName(%q, %q) = %v", tc.p, tc.s, got)
		}
	}
}

func TestPickServices(t *testing.T) {
	list := []svcEntry{
		{Name: "VeeamBackupSvc", Display: "Veeam Backup Service", Active: true},
		{Name: "VeeamDeploySvc", Display: "Veeam Installer Service", Active: false}, // manual, stopped
		{Name: "VeeamTransportSvc", Display: "Veeam Data Mover", Active: false},     // automatic, stopped
		{Name: "VeeamUpdateSvc", Display: "Veeam Update", Active: true},
		{Name: "Spooler", Display: "Druckwarteschlange", Active: true},
		{Name: "MSSQLSERVER", Display: "SQL Server (MSSQLSERVER)", Active: true},
	}
	auto := func(e svcEntry) bool { return e.Name == "VeeamTransportSvc" }

	// Required names: found or missing; patterns and central entries: running or automatic ones.
	got := pickKeys(pickServices(list, []string{"spooler", "W3SVC", "veeam*"}, []string{"SQL Server*"}, []string{"VeeamUpdate*"}, auto))
	want := []string{"svc:spooler", "svc:W3SVC!", "svc:MSSQLSERVER?", "svc:VeeamBackupSvc?", "svc:VeeamTransportSvc?"}
	if !slices.Equal(got, want) {
		t.Fatalf("picked %v, want %v", got, want)
	}

	// A central entry without a match reports nothing; a required name is never ignored.
	got = pickKeys(pickServices(list, []string{"Spooler"}, []string{"backupexec*", "nothere"}, []string{"spool*"}, auto))
	if !slices.Equal(got, []string{"svc:Spooler"}) {
		t.Fatalf("picked %v", got)
	}

	// Each service once: a name in both lists is the required one.
	got = pickKeys(pickServices(list, []string{"MSSQLSERVER"}, []string{"*sql*"}, nil, auto))
	if !slices.Equal(got, []string{"svc:MSSQLSERVER"}) {
		t.Fatalf("picked %v", got)
	}

	// Linux units: matched with and without ".service", keyed without it.
	units := []svcEntry{{Name: "veeamservice.service", Display: "Veeam Agent for Linux", Active: true}, {Name: "ssh.service", Active: true}}
	got = pickKeys(pickServices(units, nil, []string{"veeam*", "ssh"}, nil, nil))
	if !slices.Equal(got, []string{"svc:ssh?", "svc:veeamservice?"}) {
		t.Fatalf("units picked %v", got)
	}
}

func TestParseUnits(t *testing.T) {
	lu := `ssh.service                 loaded    active   running OpenBSD Secure Shell server
● veeamservice.service      loaded    failed   failed  Veeam Agent for Linux service
nothere.service             not-found inactive dead    nothere.service
cron.service                loaded    inactive dead    Regular background program processing daemon
`
	units := parseListUnits(lu)
	if len(units) != 3 || units["veeamservice.service"].Active != "failed" || units["ssh.service"].Description != "OpenBSD Secure Shell server" {
		t.Fatalf("units %+v", units)
	}
	uf := `ssh.service              enabled         enabled
cron.service             disabled        enabled
getty@.service           enabled         enabled
veeamservice.service     enabled-runtime enabled
systemd-fsck@.service    static          -
`
	en := parseEnabledUnits(uf)
	if len(en) != 2 || !en["ssh.service"] || !en["veeamservice.service"] {
		t.Fatalf("enabled %v", en)
	}
}
