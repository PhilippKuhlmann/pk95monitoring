package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"pk95monitoring/internal/fake/fakepve"
	"pk95monitoring/internal/model"
	"pk95monitoring/internal/store"
)

// pveParent imports a Proxmox device backed by fakepve (nodes pve1–pve3) and polls it once.
func (e *env) pveParent(t *testing.T) model.Device {
	t.Helper()
	pve := fakepve.New()
	t.Cleanup(pve.Close)
	cfg := pve.Config()
	d := model.Device{ID: "pve", Name: "Cluster", Type: model.TypeProxmox, Host: pve.Host(), Enabled: true, Proxmox: &cfg}
	if err := e.c.ImportDevice(d); err != nil {
		t.Fatal(err)
	}
	if _, err := e.c.PollNow(context.Background(), d.ID); err != nil {
		t.Fatal(err)
	}
	if len(e.c.pveNodes(d.ID)) == 0 {
		t.Fatal("no nodes polled")
	}
	return d
}

func rowOf(rows []DeviceRow, id string) (DeviceRow, bool) {
	for _, r := range rows {
		if r.ID == id {
			return r, true
		}
	}
	return DeviceRow{}, false
}

func TestAttachDetach(t *testing.T) {
	e := newEnv(t, Options{})
	p := e.pveParent(t)
	ag := e.agentDevice(t, "ag1", func(d *model.Device) { d.Agent.Hostname = "PVE1.example.lan"; d.Agent.Version = "1.2.0" })
	e.agentDevice(t, "ag2", func(d *model.Device) { d.Agent.Hostname = "other" })

	// Suggestions by host name (short, case-insensitive).
	sug := e.c.Suggest(ag)
	if len(sug) != 1 || sug[0].ID != p.ID || sug[0].Node != "pve1" || sug[0].Type != model.TypeProxmox {
		t.Fatalf("suggest %+v", sug)
	}
	if s := e.c.Suggest("ag2"); len(s) != 0 {
		t.Errorf("suggest without match %+v", s)
	}

	// Validation.
	for _, tc := range []struct {
		parent, agent, node, field string
	}{
		{p.ID, "", "", "agent_id"},
		{p.ID, "nope", "", "agent_id"},
		{p.ID, p.ID, "", "agent_id"},
		{"ag2", ag, "", "agent_id"},
		{p.ID, ag, "pve9", "node"},
		{p.ID, ag, "pve\x01", "node"},
	} {
		_, errs, err := e.c.Attach(tc.parent, tc.agent, tc.node)
		if err != nil || errs[tc.field] == "" {
			t.Errorf("Attach(%q, %q, %q): %v %v", tc.parent, tc.agent, tc.node, errs, err)
		}
	}
	if _, _, err := e.c.Attach("nope", ag, ""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown parent: %v", err)
	}

	// Node defaults to the node named like the host.
	got, errs, err := e.c.Attach(p.ID, ag, "")
	if err != nil || len(errs) > 0 || got.ID != p.ID {
		t.Fatalf("attach: %v %v", errs, err)
	}
	if d, _ := e.c.Device(ag); d.ParentID != p.ID || d.Node != "pve1" {
		t.Fatalf("attached device %+v", d)
	}
	if s := e.c.Suggest(ag); len(s) != 0 {
		t.Errorf("suggest for attached agent %+v", s)
	}
	// Another agent on the same node is refused.
	if _, errs, _ := e.c.Attach(p.ID, "ag2", "PVE1"); errs["node"] == "" {
		t.Errorf("duplicate node accepted: %v", errs)
	}
	if list := e.c.Attached(p.ID); len(list) != 1 || list[0].ID != ag {
		t.Errorf("attached %+v", list)
	}

	// Lists hide the attached agent; AllRows shows it with the parent.
	rows := e.c.Rows()
	if _, ok := rowOf(rows, ag); ok {
		t.Error("attached agent in Rows")
	}
	if r, _ := rowOf(rows, p.ID); r.Attached != 1 {
		t.Errorf("parent row %+v", r)
	}
	if r, ok := rowOf(e.c.AllRows(), ag); !ok || r.ParentID != p.ID || r.ParentName != "Cluster" {
		t.Errorf("all rows %+v", r)
	}

	// SaveDevice keeps ParentID/Node and rejects a ParentID pointing to an agent or itself.
	d, _ := e.c.Device(ag)
	d.ParentID, d.Node, d.Name = "", "", "pve1-agent"
	if _, errs, err := e.c.SaveDevice(d); err != nil || len(errs) > 0 {
		t.Fatalf("save: %v %v", errs, err)
	}
	if d, _ := e.c.Device(ag); d.ParentID != p.ID || d.Node != "pve1" || d.Name != "pve1-agent" {
		t.Errorf("save lost the parent: %+v", d)
	}
	d.ParentID = "ag2"
	if _, errs, _ := e.c.SaveDevice(d); errs["parent_id"] == "" {
		t.Errorf("parent agent accepted: %v", errs)
	}
	d.ParentID = ag
	if _, errs, _ := e.c.SaveDevice(d); errs["parent_id"] == "" {
		t.Errorf("parent itself accepted: %v", errs)
	}

	// Detach.
	if d, err := e.c.Detach(ag); err != nil || d.ParentID != "" || d.Node != "" {
		t.Fatalf("detach %+v %v", d, err)
	}
	if _, ok := rowOf(e.c.Rows(), ag); !ok {
		t.Error("detached agent not listed")
	}
	if _, err := e.c.Detach("nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("detach unknown: %v", err)
	}
}

func TestAttachedState(t *testing.T) {
	e := newEnv(t, Options{})
	p := e.pveParent(t)
	ag := e.agentDevice(t, "ag1", func(d *model.Device) { d.Agent.Hostname = "pve1"; d.Agent.Version = "1.2.0" })
	if _, errs, err := e.c.Attach(p.ID, ag, "pve1"); err != nil || len(errs) > 0 {
		t.Fatalf("attach: %v %v", errs, err)
	}
	parentOwn := e.c.Status(p.ID)
	before, _ := rowOf(e.c.Rows(), p.ID)

	disk := metricCheck("disk:/", "Festplatte /", model.Metric{Name: "disk.usage", Instance: "/", Value: 97, Unit: "%"})
	e.report(t, ag, disk)
	e.wait()

	r, _ := rowOf(e.c.Rows(), p.ID)
	if r.State != model.StateCrit || !strings.HasPrefix(r.Message, "5 Probleme: ") ||
		!strings.Contains(r.Message, "; pve1: Festplatte /: Festplattenbelegung / 97 % ≥ 95 %") {
		t.Errorf("parent row %s %q (own %s %q)", r.State, r.Message, parentOwn.State, parentOwn.Message)
	}
	if r.Checks.Crit != before.Checks.Crit+1 || r.Checks.OK != before.Checks.OK+2 { // disk crit, reach and discovery ok
		t.Errorf("check counts %+v, before %+v", r.Checks, before.Checks)
	}
	ov, err := e.c.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.Devices) != 1 || ov.Counts.Crit != 1 {
		t.Errorf("overview %d devices, counts %+v", len(ov.Devices), ov.Counts)
	}
	sum := e.c.Summary()
	if len(sum.Devices) != 1 || sum.Counts["crit"] != 1 || sum.State != model.StateCrit {
		t.Errorf("summary %+v %+v", sum.Devices, sum.Counts)
	}

	// The agent keeps its own alert, named "<parent> / <node>".
	alerts, _ := e.st.ActiveAlerts()
	found := false
	for _, a := range alerts {
		if a.DeviceID == ag && a.CheckKey == "disk:/" {
			found = a.DeviceName == "Cluster / pve1"
		}
	}
	if !found {
		t.Errorf("agent alert %+v", alerts)
	}
	sent := false
	for _, title := range e.rec.titles() {
		if strings.Contains(title, "Kritisch: Cluster / pve1 – Festplatte /") {
			sent = true
		}
	}
	if !sent {
		t.Error("notification without parent name")
	}

	// The Proxmox node check shows the agent.
	ch, ok := findCheck(e.c.Status(p.ID), "pve:node:pve1")
	if !ok || ch.Facts["Agent"] != "ag1 · Kritisch · v1.2.0" {
		t.Errorf("node fact %q", ch.Facts["Agent"])
	}
	if ch, _ := findCheck(e.c.Status(p.ID), "pve:node:pve2"); ch.Facts["Agent"] != "" {
		t.Error("fact on another node")
	}
	e.c.mu.Lock()
	nodeCS, _ := findCheck(e.c.rt[p.ID].st, "pve:node:pve1")
	e.c.mu.Unlock()
	if nodeCS.Facts["Agent"] != "" {
		t.Error("fact leaked into the stored status")
	}

	// A disabled agent does not colour its parent.
	d, _ := e.c.Device(ag)
	d.Enabled = false
	if _, errs, err := e.c.SaveDevice(d); err != nil || len(errs) > 0 {
		t.Fatal(errs, err)
	}
	if r, _ := rowOf(e.c.Rows(), p.ID); r.State != parentOwn.State {
		t.Errorf("disabled agent counts: %s", r.State)
	}

	// Deleting the parent detaches the agent.
	if err := e.c.DeleteDevice(p.ID); err != nil {
		t.Fatal(err)
	}
	if d, err := e.c.Device(ag); err != nil || d.ParentID != "" || d.Node != "" {
		t.Errorf("after parent delete %+v %v", d, err)
	}
	stored, err := store.List[model.Device](e.st, KindDevice)
	if err != nil || len(stored) != 1 || stored[0].ID != ag || stored[0].ParentID != "" {
		t.Errorf("stored after parent delete %+v %v", stored, err)
	}
}

func TestRegisterAttach(t *testing.T) {
	e := newEnv(t, Options{})
	p := e.pveParent(t)
	e.agentDevice(t, "ag0", nil)
	key := e.c.EnrollKey()
	ctx := context.Background()
	reg := func(dev, node, host string) (model.AgentRegistered, error) {
		return e.c.RegisterAgent(ctx, model.AgentRegister{Key: key, AgentID: "m-" + host, Hostname: host, OS: "linux",
			DeviceID: dev, Node: node, IPs: []string{"10.0.10.12"}}, "10.0.10.12")
	}
	var ie *InputError
	if _, err := reg("nope", "", "pve2"); !errors.As(err, &ie) {
		t.Errorf("unknown device: %v", err)
	}
	if _, err := reg("ag0", "", "pve2"); !errors.As(err, &ie) {
		t.Errorf("agent as parent: %v", err)
	}
	if _, err := reg(p.ID, "pve9", "pve2"); !errors.As(err, &ie) {
		t.Errorf("unknown node: %v", err)
	}
	res, err := reg(p.ID, "", "PVE2.lan")
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := e.c.Device(res.DeviceID); d.ParentID != p.ID || d.Node != "pve2" {
		t.Errorf("registered %+v", d)
	}
	// Re-registration without a device keeps the attachment.
	res2, err := reg("", "", "PVE2.lan")
	if err != nil || res2.DeviceID != res.DeviceID {
		t.Fatal(res2, err)
	}
	if d, _ := e.c.Device(res.DeviceID); d.ParentID != p.ID || d.Node != "pve2" {
		t.Errorf("re-registered %+v", d)
	}
	// Suggestion by node IP for an agent with another host name.
	e.agentDevice(t, "ag3", func(d *model.Device) { d.Agent.Hostname = "x"; d.Agent.IPs = []string{"10.0.10.13"} })
	if s := e.c.Suggest("ag3"); len(s) != 1 || s[0].Node != "pve3" {
		t.Errorf("suggest by IP %+v", s)
	}
}
