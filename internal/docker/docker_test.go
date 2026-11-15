package docker

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pk95monitoring/internal/fake/fakedocker"
	"pk95monitoring/internal/model"
)

func demoContainers(now time.Time) []fakedocker.Container {
	return []fakedocker.Container{
		{ID: "a1b2c3d4e5f60001", Name: "nextcloud-app-1", Image: "nextcloud:30-apache", State: "running", Health: "healthy",
			Policy: "unless-stopped", Project: "nextcloud", Started: now.Add(-72 * time.Hour), PublicPort: 8080, PrivatePort: 80,
			CPUPct: 12.5, MemUsed: 512 << 20, NetRate: 6000},
		{ID: "a1b2c3d4e5f60002", Name: "nextcloud-db-1", Image: "mariadb:11", State: "running", Health: "unhealthy",
			HealthOut: "mysqladmin: connect to server failed", Failing: 4, Policy: "unless-stopped", Project: "nextcloud",
			Started: now.Add(-72 * time.Hour), CPUPct: 3, MemUsed: 300 << 20, MemLimit: 1 << 30},
		{ID: "a1b2c3d4e5f60003", Name: "traefik", Image: "traefik:v3.2", State: "exited", ExitCode: 1, Policy: "always",
			Finished: now.Add(-20 * time.Minute)},
		{ID: "a1b2c3d4e5f60004", Name: "backup-once", Image: "alpine", State: "exited", ExitCode: 0, Policy: "no",
			Finished: now.Add(-50 * time.Hour)},
		{ID: "a1b2c3d4e5f60005", Name: "worker", Image: "app/worker:2", State: "restarting", ExitCode: 137, Policy: "always", Restarts: 4,
			Finished: now.Add(-1 * time.Minute)},
	}
}

func client(s *fakedocker.Server) *Client { return &Client{HTTP: s.Client(), Base: s.URL} }

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

func TestQueryAndChecks(t *testing.T) {
	now := time.Now()
	srv := fakedocker.New()
	defer srv.Close()
	srv.Set(demoContainers(now)...)

	r, err := client(srv).Query(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !r.Available || r.Engine != "Docker" || r.Version != "27.3.1" || r.Images != 9 || len(r.Cts) != 5 {
		t.Fatalf("report = %+v", r)
	}
	if srv.Calls("/containers/a1b2c3d4e5f60003/stats") != 0 {
		t.Error("stats were read for a stopped container")
	}
	var app Container
	for _, c := range r.Cts {
		if c.Name == "nextcloud-app-1" {
			app = c
		}
	}
	if app.Project != "nextcloud" || app.Policy != "unless-stopped" || app.Health != "healthy" || app.Ports != "8080→80/tcp" ||
		!app.HasStats || app.CPU != 12.5 || app.MemUsed != 512<<20 || app.MemLimit != fakedocker.MemTotal {
		t.Fatalf("app = %+v", app)
	}

	cs := byKey(Checks(r, nil, now))
	host := cs[KeyHost]
	if host.State != model.StateOK || host.Message != "Docker 27.3.1 · 2 von 5 Containern laufen" {
		t.Errorf("host = %+v", host)
	}
	want := map[string]string{
		"docker:ct:nextcloud-app-1": model.StateOK,
		"docker:ct:nextcloud-db-1":  model.StateCrit,
		"docker:ct:traefik":         model.StateCrit,
		"docker:ct:backup-once":     model.StateOK,
		"docker:ct:worker":          model.StateCrit,
	}
	for k, st := range want {
		if cs[k].State != st {
			t.Errorf("%s: state %s, want %s (%s)", k, cs[k].State, st, cs[k].Message)
		}
	}
	if m := cs["docker:ct:nextcloud-app-1"].Message; m != "läuft seit 3 Tagen (gesund) · CPU 12 % · RAM 512 MiB" {
		t.Errorf("app message %q", m)
	}
	if m := cs["docker:ct:nextcloud-db-1"].Message; !strings.HasPrefix(m, "Healthcheck schlägt fehl (4× in Folge): mysqladmin: connect to server failed · läuft") {
		t.Errorf("db message %q", m)
	}
	if v, _ := metric(cs["docker:ct:nextcloud-db-1"], "guest.mem"); v < 29 || v > 30 {
		t.Errorf("db guest.mem = %v (300 MiB of 1 GiB)", v)
	}
	if m := cs["docker:ct:traefik"].Message; m != "läuft nicht – beendet mit Code 1 vor 20 Min." {
		t.Errorf("traefik message %q", m)
	}
	if m := cs["docker:ct:backup-once"].Message; m != "beendet mit Code 0 vor 2 Tagen" {
		t.Errorf("backup message %q", m)
	}
	if f := cs["docker:ct:nextcloud-app-1"].Facts; f["Compose-Projekt"] != "nextcloud" || f["Neustart-Richtlinie"] != "außer manuell gestoppt" || f["Image"] != "nextcloud:30-apache" {
		t.Errorf("facts %v", f)
	}
}

func TestTrackerRestartsAndRates(t *testing.T) {
	now := time.Now()
	srv := fakedocker.New()
	defer srv.Close()
	srv.Set(demoContainers(now)...)
	cl := client(srv)
	var tr Tracker

	r1, err := cl.Query(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tr.Update(&r1, now)
	srv.Update("worker", func(c *fakedocker.Container) { c.Restarts = 7; c.State = "running"; c.Started = now })
	r2, _ := cl.Query(context.Background())
	tr.Update(&r2, now.Add(time.Minute))
	cs := byKey(Checks(r2, nil, now.Add(time.Minute)))
	w := cs["docker:ct:worker"]
	if v, _ := metric(w, "docker.restarts"); v != 3 {
		t.Errorf("worker restarts in the last hour = %v, want 3", v)
	}
	if !strings.Contains(w.Message, "3× neu gestartet in der letzten Stunde") {
		t.Errorf("worker message %q", w.Message)
	}
	app := cs["docker:ct:nextcloud-app-1"]
	if v, ok := metric(app, "guest.netin"); !ok || v != 100 { // 6000 bytes per minute
		t.Errorf("app guest.netin = %v %v", v, ok)
	}
	// an hour later the restarts are forgotten
	r3, _ := cl.Query(context.Background())
	tr.Update(&r3, now.Add(62*time.Minute))
	for _, c := range r3.Cts {
		if c.Name == "worker" && c.Restarts1h != 0 {
			t.Errorf("worker Restarts1h after an hour = %d", c.Restarts1h)
		}
	}
	// a re-created container (new ID) does not count as restarted
	srv.Update("worker", func(c *fakedocker.Container) { c.ID = "ffff00000000aaaa"; c.Restarts = 0 })
	srv.Update("traefik", func(c *fakedocker.Container) { c.Restarts = 9 })
	r4, _ := cl.Query(context.Background())
	tr.Update(&r4, now.Add(63*time.Minute))
	for _, c := range r4.Cts {
		if (c.Name == "worker" && c.Restarts1h != 0) || (c.Name == "traefik" && c.Restarts1h != 9) {
			t.Errorf("%s Restarts1h = %d", c.Name, c.Restarts1h)
		}
	}
	if len(tr.Good().Cts) != 5 {
		t.Error("tracker did not keep the last good report")
	}
}

func TestDownAndUnreadable(t *testing.T) {
	cl := NewSocket(filepath.Join(t.TempDir(), "docker.sock"))
	defer cl.Close()
	_, err := cl.Query(context.Background())
	if !errors.Is(err, ErrDown) {
		t.Fatalf("err = %v, want ErrDown", err)
	}
	last := Report{Available: true, Engine: "Docker", Cts: []Container{{Name: "web", State: "running"}}}
	cs := byKey(Checks(last, err, time.Now()))
	if cs[KeyHost].State != model.StateCrit || cs["docker:ct:web"].State != model.StateUnknown {
		t.Errorf("checks %+v", cs)
	}
	cs = byKey(Checks(Report{}, errors.New("permission denied"), time.Now()))
	if cs[KeyHost].State != model.StateUnknown || len(cs) != 1 {
		t.Errorf("checks %+v", cs)
	}
	if len(Checks(Report{}, nil, time.Now())) != 0 {
		t.Error("a host without Docker has checks")
	}
}

func TestPodmanAndStates(t *testing.T) {
	now := time.Now()
	srv := fakedocker.New()
	defer srv.Close()
	srv.SetPodman(true)
	srv.Set(fakedocker.Container{ID: "p1", Name: "db", Image: "postgres:17", State: "paused", Policy: "always"},
		fakedocker.Container{ID: "p2", Name: "migrate", Image: "app", State: "exited", ExitCode: 0, Policy: "no", Project: "app"},
		fakedocker.Container{ID: "p3", Name: "app-web-1", Image: "app", State: "exited", ExitCode: 2, Policy: "no", Project: "app"},
		fakedocker.Container{ID: "p4", Name: "oom", Image: "app", State: "exited", ExitCode: 137, OOM: true, Policy: "on-failure"},
		fakedocker.Container{ID: "p5", Name: "fresh", Image: "app", State: "created", Policy: "no"})
	r, err := client(srv).Query(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cs := byKey(Checks(r, nil, now))
	if cs[KeyHost].Name != "Podman" || !strings.HasPrefix(cs[KeyHost].Message, "Podman 27.3.1 · 0 von 5") {
		t.Errorf("host %+v", cs[KeyHost])
	}
	want := map[string]string{"db": model.StateWarn, "migrate": model.StateOK, "app-web-1": model.StateCrit, "oom": model.StateCrit, "fresh": model.StateOK}
	for n, st := range want {
		if c := cs[KeyCt+n]; c.State != st {
			t.Errorf("%s: %s (%s), want %s", n, c.State, c.Message, st)
		}
	}
	if m := cs[KeyCt+"oom"].Message; !strings.Contains(m, "Arbeitsspeicher voll") {
		t.Errorf("oom message %q", m)
	}
}
