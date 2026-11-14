package core

import (
	"testing"

	"pk95monitoring/internal/model"
)

func TestBackupsOverview(t *testing.T) {
	e := newEnv(t, Options{})
	id := e.agentDevice(t, "srv", func(d *model.Device) { d.Group = "Server" })
	e.report(t, id,
		model.Check{Key: "pve:bak:100", Name: "Sicherung VM 100 dc01", Group: "Sicherungen", State: model.StateOK,
			Metrics: []model.Metric{{Name: "backup.age", Instance: "vm/100", Value: 10, Unit: "h"}, {Name: "backup.size", Instance: "vm/100", Value: 5e9, Unit: "B"}},
			Facts:   map[string]string{"Ziel": "pbs01", "Verifiziert": "ja"}},
		model.Check{Key: "veeam:job:x", Name: "Täglich", Group: "Jobs", State: model.StateCrit, Message: "Fehlgeschlagen",
			Metrics: []model.Metric{{Name: "job.age", Instance: "Täglich", Value: 3, Unit: "h"}},
			Facts:   map[string]string{"Ergebnis": "Fehlgeschlagen", "Nächster Lauf": "15.11.2026 22:00", "Typ": "VM-Sicherung"}},
		model.Check{Key: "veeam:repo:r", Name: "Repository NAS", Group: "Repositories", State: model.StateOK,
			Metrics: []model.Metric{{Name: "storage.usage", Instance: "NAS", Value: 71, Unit: "%"}}},
		model.Check{Key: "cpu", Name: "CPU", Group: "System", State: model.StateOK},
	)
	ov := e.c.Backups()
	if len(ov.Rows) != 2 || ov.Rows[0].CheckKey != "veeam:job:x" || ov.Rows[0].Kind != "job" || ov.Rows[0].Result != "Fehlgeschlagen" ||
		ov.Rows[0].Next == "" || ov.Rows[0].JobType != "VM-Sicherung" || *ov.Rows[0].Age != 3 {
		t.Fatalf("rows: %+v", ov.Rows)
	}
	g := ov.Rows[1]
	if g.Kind != "guest" || g.Target != "pbs01" || g.Result != "Verifiziert: ja" || *g.Size != 5e9 || g.Group != "Server" ||
		g.Last != e.clk.Now().Unix()-10*3600 {
		t.Fatalf("guest: %+v", g)
	}
	if len(ov.Storages) != 1 || ov.Storages[0].Name != "NAS" || *ov.Storages[0].Usage != 71 {
		t.Fatalf("storages: %+v", ov.Storages)
	}
}

// Veeam servers are listed under Sicherungen, not in the device list; their checks are separate.
func TestVeeamServerOnBackupPage(t *testing.T) {
	e := newEnv(t, Options{})
	d := model.Device{ID: "vbr", Name: "Veeam", Type: model.TypeVeeam, Host: "10.0.0.5", Enabled: true,
		Veeam: &model.VeeamConfig{Port: 9419, Username: "m", Password: "p"}}
	if err := e.c.ImportDevice(d); err != nil {
		t.Fatal(err)
	}
	if _, ok := rowOf(e.c.Rows(), "vbr"); ok {
		t.Fatal("Veeam server in the device list")
	}
	ov := e.c.Backups()
	if len(ov.Servers) != 1 || ov.Servers[0].ID != "vbr" || ov.Servers[0].Host != "10.0.0.5" {
		t.Fatalf("servers: %+v", ov.Servers)
	}
	for _, k := range []string{"veeam:job:1", "veeam:repo:1", "pve:bak:100", "pve:backup"} {
		if !model.SeparateCheck(k) || alertKind(k) != NotifyBackups {
			t.Errorf("%s not a separate backup check", k)
		}
	}
}
