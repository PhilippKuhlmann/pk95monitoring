package core

import (
	"context"
	"testing"

	"pk95monitoring/internal/fake/fakeopnsense"
	"pk95monitoring/internal/model"
)

// An OPNsense firewall is an ordinary device; its firmware check appears on the updates page.
func TestOPNsenseDevice(t *testing.T) {
	e := newEnv(t, Options{})
	srv := fakeopnsense.New()
	t.Cleanup(srv.Close)
	cfg := srv.Config()
	_, errs, _ := e.c.SaveDevice(model.Device{Name: "x", Type: model.TypeOPNsense, OPNsense: &model.OPNsenseConfig{Fingerprint: "zz", Port: 70000}})
	if errs["host"] == "" || errs["opnsense.key"] == "" || errs["opnsense.secret"] == "" || errs["opnsense.fingerprint"] == "" || errs["opnsense.port"] == "" {
		t.Fatalf("validation %v", errs)
	}
	d, errs, err := e.c.SaveDevice(model.Device{Name: "fw-filiale", Type: model.TypeOPNsense, Host: srv.Host(), Enabled: true, OPNsense: &cfg})
	if err != nil || len(errs) > 0 {
		t.Fatalf("save: %v %v", errs, err)
	}
	d.OPNsense.Secret = "" // an edit without secret keeps it
	if d, errs, err = e.c.SaveDevice(d); err != nil || len(errs) > 0 || d.OPNsense.Secret != fakeopnsense.Secret {
		t.Fatalf("edit: %v %v", errs, err)
	}
	st, err := e.c.PollNow(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	// pending updates are listed on the updates page and leave the device state alone
	if up, _ := findCheck(st, "updates"); st.State != model.StateOK || up.State != model.StateWarn {
		t.Errorf("state %s, updates %+v", st.State, up)
	}
	for _, k := range []string{"reach", "opn:system", "opn:gw:WAN_TELEKOM", "opn:ipsec:Filiale Süd", "opn:carp", "opn:svc:unbound", "updates"} {
		if _, ok := findCheck(st, k); !ok {
			t.Errorf("missing %s", k)
		}
	}
	rows := e.c.Updates()
	if len(rows) != 1 || rows[0].DeviceID != d.ID || rows[0].Pending == nil || *rows[0].Pending != 3 {
		t.Errorf("updates page %+v", rows)
	}
	if _, err := e.c.TestOPNsense(context.Background(), d.ID, srv.Host(), model.OPNsenseConfig{Port: cfg.Port, Key: cfg.Key, Fingerprint: cfg.Fingerprint}); err != nil {
		t.Errorf("test with the stored secret: %v", err)
	}
	srv.SetGateway("WAN_TELEKOM", true)
	st, _ = e.c.PollNow(context.Background(), d.ID)
	if gw, _ := findCheck(st, "opn:gw:WAN_TELEKOM"); gw.State != model.StateCrit || st.State != model.StateCrit {
		t.Errorf("gateway down: %+v (device %s)", gw, st.State)
	}
}
