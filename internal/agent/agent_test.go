package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"pk95monitoring/internal/model"
)

// fakeServer implements the agent endpoints of the monitoring server.
type fakeServer struct {
	mu      sync.Mutex
	status  int // forced status for /report (0 = OK)
	reports []model.AgentReport
	reg     model.AgentRegister
	reply   model.AgentReply
}

func (f *fakeServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/agent/v1/register":
			if err := json.NewDecoder(r.Body).Decode(&f.reg); err != nil {
				t.Error(err)
			}
			if f.reg.Key != "enroll-key" {
				http.Error(w, `{"error":"falscher Schlüssel"}`, http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(model.AgentRegistered{DeviceID: "dev1", Token: "tok1", Interval: 60})
		case "/agent/v1/report":
			if r.Header.Get("Authorization") != "Bearer tok1" {
				http.Error(w, `{"error":"token"}`, http.StatusUnauthorized)
				return
			}
			if f.status != 0 {
				http.Error(w, `{"error":"forced"}`, f.status)
				return
			}
			var rep model.AgentReport
			if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
				t.Error(err)
			}
			f.reports = append(f.reports, rep)
			json.NewEncoder(w).Encode(f.reply)
		default:
			http.NotFound(w, r)
		}
	}
}

func (f *fakeServer) set(status int) {
	f.mu.Lock()
	f.status = status
	f.mu.Unlock()
}

func (f *fakeServer) take() []model.AgentReport {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.reports
	f.reports = nil
	return out
}

// seqReporter returns reports with increasing Time.
type seqReporter struct{ n int64 }

func (s *seqReporter) Report(context.Context, Config) model.AgentReport {
	s.n++
	return model.AgentReport{AgentID: "m1", Hostname: "host", Version: Version, Time: s.n,
		Checks: []model.Check{{Key: "cpu", Name: "CPU", State: model.StateOK}}}
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRegisterAndReport(t *testing.T) {
	fs := &fakeServer{reply: model.AgentReply{Interval: 20, Services: []string{"sshd"}, IgnoreServices: []string{"x*"}}}
	srv, fp := tlsServer(t, fs.handler(t))
	ctx := context.Background()

	if _, err := Register(ctx, srv.URL, "wrong", fp, false); err == nil {
		t.Fatal("wrong key accepted")
	}
	reg, err := Register(ctx, srv.URL, "enroll-key", colonUpper(fp), false)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Token != "tok1" || reg.DeviceID != "dev1" || fs.reg.AgentID == "" || fs.reg.Hostname == "" || fs.reg.Version != Version {
		t.Fatalf("registration: %+v / %+v", reg, fs.reg)
	}
	if fs.reg.DeviceID != "" || fs.reg.Node != "" {
		t.Errorf("plain registration attaches: %+v", fs.reg)
	}
	if _, err := RegisterDevice(ctx, srv.URL, "enroll-key", fp, false, " demo-pve ", "pve1"); err != nil {
		t.Fatal(err)
	}
	if fs.reg.DeviceID != "demo-pve" || fs.reg.Node != "pve1" {
		t.Errorf("device and node not sent: %+v", fs.reg)
	}

	cfgPath := filepath.Join(t.TempDir(), "agent.json")
	if err := Save(cfgPath, Config{Server: srv.URL, Token: reg.Token, DeviceID: reg.DeviceID, Fingerprint: fp, Interval: reg.Interval}); err != nil {
		t.Fatal(err)
	}
	a, err := New(cfgPath, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	a.Collector = &seqReporter{}
	a.Updater = nil

	// Server down: reports 1 and 2 are buffered.
	fs.set(http.StatusServiceUnavailable)
	for range 2 {
		if _, err := a.Cycle(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if a.Buffer.Len() != 2 {
		t.Fatalf("buffered %d, want 2", a.Buffer.Len())
	}

	// Back up: live report 3 first, then 1 and 2 with buffered=true; reply applied and saved.
	fs.set(0)
	wait, err := a.Cycle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := fs.take()
	if len(got) != 3 || got[0].Time != 3 || got[0].Buffered || got[1].Time != 1 || !got[1].Buffered || got[2].Time != 2 || !got[2].Buffered {
		t.Fatalf("delivery order/flags wrong: %+v", got)
	}
	if a.Buffer.Len() != 0 {
		t.Errorf("buffer not empty: %d", a.Buffer.Len())
	}
	if wait != 20*time.Second {
		t.Errorf("wait %v, want 20s from the reply", wait)
	}
	saved, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Interval != 20 || !slices.Equal(saved.Services, []string{"sshd"}) || !slices.Equal(saved.IgnoreServices, []string{"x*"}) {
		t.Errorf("reply not persisted: %+v", saved)
	}

	// Permanent rejection: dropped, not buffered.
	fs.set(http.StatusBadRequest)
	if _, err := a.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	if a.Buffer.Len() != 0 {
		t.Errorf("400 report was buffered")
	}

	// Token rejected: buffered, and the agent slows down.
	a.Client, _ = NewClient(srv.URL, "revoked", fp, false)
	fs.set(0)
	wait, _ = a.Cycle(ctx)
	if wait != unauthorizedRetry || a.Buffer.Len() != 1 {
		t.Errorf("401: wait %v, buffered %d", wait, a.Buffer.Len())
	}
}

func TestCycleUpdateWithWrongHashKeepsRunning(t *testing.T) {
	fs := &fakeServer{reply: model.AgentReply{Interval: 60, Update: &model.AgentUpdate{
		Version: "9.9.9", URL: "/agent/download/x/y", SHA256: "00"}}}
	srv, fp := tlsServer(t, fs.handler(t))
	c, _ := NewClient(srv.URL, "tok1", fp, false)
	exe := writeExe(t, "old")
	a := &Agent{Client: c, Collector: &seqReporter{}, Log: quietLog(),
		Updater: &Updater{Client: c, Exe: exe, Version: "1.0.0", Log: quietLog()}}
	if _, err := a.Cycle(context.Background()); err != nil {
		t.Fatalf("failed update must not stop the agent: %v", err)
	}
	if readFile(t, exe) != "old" {
		t.Error("binary replaced")
	}
}

func TestCycleBuffersReportWhenStopped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	srv, fp := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
		cancel() // the service stops while the request is in flight
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) }) // runs before the server's Close
	c, _ := NewClient(srv.URL, "tok1", fp, false)
	buf, err := OpenBuffer(filepath.Join(t.TempDir(), "buffer.jsonl"), 10)
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{Client: c, Collector: &seqReporter{}, Buffer: buf, Log: quietLog()}
	if _, err := a.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 1 {
		t.Errorf("report interrupted by shutdown not buffered: %d", buf.Len())
	}
}
