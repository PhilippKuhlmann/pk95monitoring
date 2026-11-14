package core

import (
	"testing"

	"pk95monitoring/internal/model"
)

// The SNMP view of a UniFi console is attached to the console: one row, combined state.
func TestAttachDevice(t *testing.T) {
	e := newEnv(t, Options{})
	imp := func(d model.Device) string {
		t.Helper()
		d.Enabled, d.Created = true, e.clk.Now().Unix()
		if err := e.c.ImportDevice(d); err != nil {
			t.Fatal(err)
		}
		return d.ID
	}
	udm := imp(model.Device{ID: "udm", Name: "UDM Pro", Type: model.TypeUniFi, Host: "10.0.0.1", UniFi: &model.UniFiConfig{Username: "m", Password: "p"}})
	snmp := imp(model.Device{ID: "udm-snmp", Name: "UDM SNMP", Type: model.TypeSNMP, Host: "10.0.0.1", SNMP: &model.SNMPConfig{Community: "public"}})
	other := imp(model.Device{ID: "sw", Name: "Switch", Type: model.TypeSNMP, Host: "10.0.0.2", SNMP: &model.SNMPConfig{Community: "public"}})
	m365 := imp(model.Device{ID: "m365", Name: "M365", Type: model.TypeM365, M365: &model.M365Config{TenantID: "t", ClientID: "c", Secret: "s"}})

	if s := e.c.Suggest(snmp); len(s) != 1 || s[0].ID != udm {
		t.Fatalf("suggest for the SNMP view: %+v", s)
	}
	if s := e.c.Suggest(udm); len(s) != 0 {
		t.Errorf("the console gets no suggestion: %+v", s)
	}
	if _, errs, _ := e.c.Attach(udm, m365, ""); errs["agent_id"] == "" {
		t.Error("Microsoft 365 attached")
	}
	if _, errs, _ := e.c.Attach(udm, udm, ""); errs["agent_id"] == "" {
		t.Error("attached to itself")
	}
	if _, errs, err := e.c.Attach(udm, snmp, ""); err != nil || len(errs) > 0 {
		t.Fatalf("attach: %v %v", errs, err)
	}
	if lastEventBody(t, e, "Gerät angehängt") != "Das Gerät UDM SNMP gehört jetzt zu UDM Pro." {
		t.Error("event")
	}
	rows := e.c.Rows()
	if _, ok := rowOf(rows, snmp); ok {
		t.Error("attached device still listed")
	}
	if r, _ := rowOf(rows, udm); r.Attached != 1 {
		t.Errorf("parent row %+v", r)
	}
	// one level only
	if _, errs, _ := e.c.Attach(snmp, other, ""); errs["agent_id"] == "" {
		t.Error("attached to an attached device")
	}
	if _, errs, _ := e.c.Attach(other, udm, ""); errs["agent_id"] == "" {
		t.Error("a parent with attached devices attached")
	}
	if s := e.c.Suggest(snmp); len(s) != 0 {
		t.Errorf("suggestion for an attached device: %+v", s)
	}
	if _, err := e.c.Detach(snmp); err != nil {
		t.Fatal(err)
	}
	if _, ok := rowOf(e.c.Rows(), snmp); !ok || lastEventBody(t, e, "Gerät gelöst") == "" {
		t.Error("detach")
	}
}
