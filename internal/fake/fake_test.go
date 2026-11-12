package fake

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"pk95monitoring/internal/core"
	"pk95monitoring/internal/model"
	"pk95monitoring/internal/store"
)

// TestDemoAttachedAgent: the demo has the agent pve1 attached to the Proxmox cluster, and a
// second start (existing demo database) keeps it attached.
func TestDemoAttachedAgent(t *testing.T) {
	BackfillDays = 0
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "demo.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for range 2 {
		c, err := core.New(st, core.Options{Version: "1.0.0", AgentDir: dir, Demo: true})
		if err != nil {
			t.Fatal(err)
		}
		stop, err := Setup(c)
		if err != nil {
			t.Fatal(err)
		}
		d, err := c.Device("demo-pve1")
		if err != nil || d.ParentID != "demo-pve" || d.Node != "pve1" {
			t.Fatalf("demo agent %+v %v", d, err)
		}
		if _, err := c.PollNow(context.Background(), "demo-pve"); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, cs := range c.Status("demo-pve").Checks {
			if cs.Key == "pve:node:pve1" {
				found = strings.HasPrefix(cs.Facts["Agent"], "pve1 · ")
			}
		}
		if !found {
			t.Error("pve:node:pve1 without the agent fact")
		}
		for _, r := range c.Rows() {
			if r.ID == "demo-pve1" {
				t.Error("attached demo agent listed")
			}
			if r.ID == "demo-pve" && r.Attached != 1 {
				t.Errorf("cluster row attached=%d", r.Attached)
			}
		}
		var svc []string
		for _, cs := range c.Status("demo-pve1").Checks {
			if strings.HasPrefix(cs.Key, "svc:") || strings.HasPrefix(cs.Key, "disk:") {
				svc = append(svc, cs.Key)
			}
		}
		if strings.Join(svc, ",") != "disk:/,disk:/var/lib/vz,svc:pveproxy,svc:pvedaemon,svc:corosync" {
			t.Errorf("demo agent checks %v", svc)
		}
		stop()
	}
}

// TestDemoUniFiAndNUT: the demo has a UniFi PoE switch and a UniFi UPS on a NUT server;
// both are polled and back-filled, and a demo device added to an existing database (no
// history yet) is back-filled on the next start.
func TestDemoUniFiAndNUT(t *testing.T) {
	BackfillDays, backfillStep = 1, 4*3600
	defer func() { BackfillDays, backfillStep = 0, 300 }()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "demo.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for run := range 2 {
		c, err := core.New(st, core.Options{Version: "1.0.0", AgentDir: dir, Demo: true})
		if err != nil {
			t.Fatal(err)
		}
		stop, err := Setup(c)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"demo-unifi", "demo-nut", "demo-pve1"} {
			list, err := st.SeriesList(id)
			if err != nil || len(list) == 0 {
				t.Fatalf("run %d: %s has no history (%v)", run, id, err)
			}
		}
		if run == 0 {
			// Simulate an existing database from a version without the NUT UPS.
			if err := st.DeleteDeviceData("demo-nut"); err != nil {
				t.Fatal(err)
			}
		}

		if _, err := c.PollNow(context.Background(), "demo-unifi"); err != nil {
			t.Fatal(err)
		}
		keys := map[string]model.CheckStatus{}
		for _, cs := range c.Status("demo-unifi").Checks {
			keys[cs.Key] = cs
		}
		if keys["snmp:system"].Facts["Profil"] != "UniFi-Switch" {
			t.Errorf("unifi profile facts %v", keys["snmp:system"].Facts)
		}
		for _, k := range []string{"snmp:poe", "snmp:if:0/13", "snmp:if:0/25", "snmp:cpu", "snmp:mem"} {
			if _, ok := keys[k]; !ok {
				t.Errorf("demo-unifi without %s", k)
			}
		}
		if keys["snmp:if:0/13"].State != model.StateOK || keys["snmp:if:0/1"].Facts["Nachbar"] == "" {
			t.Errorf("unifi ports: %+v / %+v", keys["snmp:if:0/13"], keys["snmp:if:0/1"].Facts)
		}

		if _, err := c.PollNow(context.Background(), "demo-nut"); err != nil {
			t.Fatal(err)
		}
		st := c.Status("demo-nut")
		ok := false
		for _, cs := range st.Checks {
			if cs.Key == "nut:ups" {
				ok = cs.State == model.StateOK && cs.Group == "USV"
			}
		}
		if !ok {
			t.Errorf("demo-nut: %+v (error %q)", st.Checks, st.Error)
		}
		d, _ := c.Device("demo-nut")
		if d.Group != groupInfra || d.NUT == nil || d.NUT.UPS != demoUPSName {
			t.Errorf("demo-nut device %+v", d)
		}
		stop()
	}
}
