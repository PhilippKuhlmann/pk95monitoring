package core

import (
	"context"
	"testing"

	"pk95monitoring/internal/fake/fakeredfish"
	"pk95monitoring/internal/model"
)

// A Redfish controller is an ordinary device: listed, coloured by its hardware checks.
func TestRedfishDevice(t *testing.T) {
	e := newEnv(t, Options{})
	srv := fakeredfish.New()
	t.Cleanup(srv.Close)
	cfg := srv.Config()
	cfg.Port = 0 // validation fills in the default
	d, errs, err := e.c.SaveDevice(model.Device{Name: "iDRAC", Type: model.TypeRedfish, Host: srv.Host(), Enabled: true, Redfish: &cfg})
	if err != nil || len(errs) > 0 {
		t.Fatalf("save: %v %v", errs, err)
	}
	if d.Redfish.Port != 443 {
		t.Fatalf("default port %d", d.Redfish.Port)
	}
	d.Redfish.Port, d.Redfish.Password = srv.Port(), "" // an edit without password keeps it
	if d, errs, err = e.c.SaveDevice(d); err != nil || len(errs) > 0 || d.Redfish.Password != fakeredfish.Password {
		t.Fatalf("edit: %v %v %+v", errs, err, d.Redfish)
	}
	st, err := e.c.PollNow(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != model.StateCrit {
		t.Errorf("device state %s (power supply 2 failed)", st.State)
	}
	row, ok := rowOf(e.c.Rows(), d.ID)
	if !ok || row.Type != model.TypeRedfish {
		t.Fatalf("not in the device list: %+v", row)
	}
	keys := map[string]bool{}
	for _, cs := range st.Checks {
		keys[cs.Key] = true
	}
	for _, k := range []string{"rf:system", "rf:power", "rf:fans", "rf:temps", "rf:memory", "rf:cpu", "rf:ctrl:RAID.Integrated.1-1"} {
		if !keys[k] {
			t.Errorf("missing %s", k)
		}
	}
	_, errs, _ = e.c.SaveDevice(model.Device{Name: "x", Type: model.TypeRedfish, Host: "h", Redfish: &model.RedfishConfig{Username: "a:b", Fingerprint: "zz"}})
	if errs["redfish.username"] == "" || errs["redfish.password"] == "" || errs["redfish.fingerprint"] == "" {
		t.Errorf("validation %v", errs)
	}
}
