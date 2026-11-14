package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"pk95monitoring/internal/model"
	"pk95monitoring/internal/netscan"
)

func waitScan(t *testing.T, e *env) ScanState {
	t.Helper()
	for range 200 {
		if st := e.c.ScanState(); !st.Running {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("scan did not finish")
	return ScanState{}
}

func TestNetworkScan(t *testing.T) {
	e := newEnv(t, Options{})
	e.agentDevice(t, "srv1", func(d *model.Device) { d.Host = "dc01"; d.Agent.IPs = []string{"10.0.0.11/24", "172.17.0.1"} })
	if _, errs, err := e.c.SaveDevice(model.Device{Name: "Switch", Type: model.TypeNet, Host: "10.0.5.2", Enabled: true,
		Checks: []model.NetCheck{{Type: model.NetPing}}}); err != nil || len(errs) > 0 {
		t.Fatal(errs, err)
	}
	st := e.c.ScanState()
	if len(st.Defaults) != 2 || st.Defaults[0] != "10.0.0.0/24" || st.Defaults[1] != "10.0.5.0/24" || st.Community != "public" || st.Running {
		t.Fatalf("defaults %+v", st)
	}
	release := make(chan struct{})
	var got netscan.Options
	e.c.Scanner = func(ctx context.Context, o netscan.Options) []netscan.Host {
		got = o
		o.Progress(1, len(o.Addrs))
		<-release
		hosts := []netscan.Host{{IP: "10.0.0.11", Ports: []int{netscan.PortSMB}}, {IP: "10.0.5.2", Ping: true}, {IP: "10.0.0.40", Ports: []int{netscan.PortPVE}}}
		for i := range hosts {
			netscan.Classify(&hosts[i])
		}
		return hosts
	}
	if errs, _ := e.c.StartScan([]string{"10.0.0.0/16"}, "public"); errs["ranges"] == "" {
		t.Error("a /16 accepted")
	}
	if errs, _ := e.c.StartScan(nil, "public"); errs["ranges"] == "" {
		t.Error("nothing to scan accepted")
	}
	if errs, err := e.c.StartScan([]string{"10.0.0.0/24", "10.0.5.2"}, "public"); err != nil || len(errs) > 0 {
		t.Fatal(errs, err)
	}
	if _, err := e.c.StartScan([]string{"10.0.0.0/24"}, ""); !errors.Is(err, ErrScanRunning) {
		t.Errorf("second scan: %v", err)
	}
	for i := 0; i < 300 && e.c.ScanState().Done == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if st := e.c.ScanState(); !st.Running || st.Done != 1 || st.Total != 255 || len(st.Ranges) != 2 {
		t.Errorf("running %+v", st)
	}
	close(release)
	st = waitScan(t, e)
	if got.Community != "public" || len(got.Addrs) != 255 || len(st.Hosts) != 3 || st.Finished == 0 {
		t.Fatalf("result %+v", st)
	}
	byIP := map[string]ScanHost{}
	for _, h := range st.Hosts {
		byIP[h.IP] = h
	}
	if h := byIP["10.0.0.11"]; h.DeviceID != "srv1" {
		t.Errorf("agent address not matched: %+v", h)
	}
	if h := byIP["10.0.5.2"]; h.DeviceName != "Switch" {
		t.Errorf("host not matched: %+v", h)
	}
	if h := byIP["10.0.0.40"]; h.DeviceID != "" || h.Suggest != model.TypeProxmox {
		t.Errorf("new host %+v", h)
	}
	if lastEventBody(t, e, "Netzwerk-Scan beendet") != "3 Geräte gefunden, 1 davon noch nicht überwacht." {
		t.Error("end event")
	}
	if err := e.c.SetScanIgnore("10.0.0.40", true); err != nil {
		t.Fatal(err)
	}
	if err := e.c.SetScanIgnore("kaputt", true); err == nil {
		t.Error("invalid address accepted")
	}
	// stored: a new engine shows the last scan and the ignored address
	c2, err := New(e.st, e.c.Opt)
	if err != nil {
		t.Fatal(err)
	}
	st2 := c2.ScanState()
	if len(st2.Hosts) != 3 || st2.Ranges[0] != "10.0.0.0/24" {
		t.Fatalf("reloaded %+v", st2)
	}
	for _, h := range st2.Hosts {
		if h.IP == "10.0.0.40" && !h.Ignored {
			t.Error("ignore not stored")
		}
	}
}
