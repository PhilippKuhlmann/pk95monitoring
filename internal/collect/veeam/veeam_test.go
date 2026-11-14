package veeam

import (
	"context"
	"strings"
	"testing"
	"time"

	"pk95monitoring/internal/fake/fakeveeam"
	"pk95monitoring/internal/model"
)

func setup(t *testing.T) (*fakeveeam.Server, *Collector, model.Device) {
	t.Helper()
	srv := fakeveeam.New()
	t.Cleanup(srv.Close)
	cfg := srv.Config()
	return srv, New(), model.Device{ID: "v1", Name: "Veeam", Type: model.TypeVeeam, Host: srv.Host(), Veeam: &cfg}
}

func byKey(list []model.Check) map[string]model.Check {
	m := map[string]model.Check{}
	for _, c := range list {
		m[c.Key] = c
	}
	return m
}

func TestCollect(t *testing.T) {
	srv, c, d := setup(t)
	checks, err := c.Collect(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	m := byKey(checks)
	if s := m["veeam:server"]; s.State != model.StateOK || s.Facts["Version"] != "12.3.1.1139" || !strings.Contains(s.Message, "5 Jobs") {
		t.Errorf("server %+v", s)
	}
	daily := m["veeam:job:a1f0c3e2-0001"]
	if daily.State != model.StateOK || daily.Name != "Täglich – VMs" || daily.Facts["Typ"] != "VM-Sicherung" || daily.Facts["Ziel"] != "Repo NAS" ||
		!strings.HasPrefix(daily.Message, "Erfolgreich · ") || !strings.Contains(daily.Message, "nächster Lauf") {
		t.Errorf("daily %+v", daily)
	}
	if len(daily.Metrics) != 1 || daily.Metrics[0].Name != "job.age" || daily.Metrics[0].Value < 9 || daily.Metrics[0].Value > 10 {
		t.Errorf("age %+v", daily.Metrics)
	}
	if w := m["veeam:job:a1f0c3e2-0003"]; w.State != model.StateWarn || !strings.HasPrefix(w.Message, "Mit Warnungen") {
		t.Errorf("warning %+v", w)
	}
	if f := m["veeam:job:a1f0c3e2-0004"]; f.State != model.StateCrit || f.Facts["Typ"] != "Agent-Richtlinie (Windows)" {
		t.Errorf("failed %+v", f)
	}
	if off := m["veeam:job:a1f0c3e2-0005"]; off.State != model.StateOK || !strings.HasPrefix(off.Message, "Deaktiviert") || off.Metrics != nil {
		t.Errorf("disabled %+v", off)
	}
	nas := m["veeam:repo:b2e1-0001"]
	if nas.Name != "Repository Repo NAS" || len(nas.Metrics) != 3 || nas.Metrics[0].Name != "storage.usage" || nas.Metrics[0].Value < 71 || nas.Metrics[0].Value > 72 {
		t.Errorf("repo %+v", nas)
	}
	if s3 := m["veeam:repo:b2e1-0002"]; s3.Message != "Kapazität unbekannt" || s3.Metrics != nil {
		t.Errorf("s3 %+v", s3)
	}

	// a second poll reuses the token; a changed result shows
	srv.SetJobResult("Täglich – VMs", "Failed")
	checks, err = c.Collect(context.Background(), d)
	if err != nil || byKey(checks)["veeam:job:a1f0c3e2-0001"].State != model.StateCrit {
		t.Fatalf("second poll: %v", err)
	}
}

func TestOverdue(t *testing.T) {
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.Local)
	j := jobState{ID: "x", Name: "Nachts", Type: "Backup", Status: "inactive", LastResult: "Success",
		LastRun: now.Add(-34 * time.Hour).Format(time.RFC3339), NextRun: now.Add(-10 * time.Hour).Format(time.RFC3339)}
	ch := jobCheck(j, now)
	if ch.State != model.StateWarn || !strings.HasPrefix(ch.Message, "Geplanter Lauf am ") {
		t.Fatalf("overdue %+v", ch)
	}
	j.Status = "running"
	if ch := jobCheck(j, now); ch.State != model.StateOK || !strings.HasPrefix(ch.Message, "Läuft gerade") {
		t.Fatalf("running %+v", ch)
	}
}

func TestErrors(t *testing.T) {
	srv, c, d := setup(t)
	bad := *d.Veeam
	bad.Password = "falsch"
	d.Veeam = &bad
	if _, err := c.Collect(context.Background(), d); err == nil || !strings.Contains(err.Error(), "Anmeldung abgelehnt") {
		t.Errorf("wrong password: %v", err)
	}
	wrongFP := srv.Config()
	wrongFP.Fingerprint = strings.Repeat("ab", 32)
	if _, err := Test(context.Background(), srv.Host(), wrongFP); err == nil || !strings.Contains(err.Error(), "Fingerabdruck") {
		t.Errorf("fingerprint: %v", err)
	}
	res, err := Test(context.Background(), srv.Host(), srv.Config())
	if err != nil || res.Version != "12.3.1.1139" || len(res.Jobs) != 5 {
		t.Errorf("test %+v %v", res, err)
	}
}
