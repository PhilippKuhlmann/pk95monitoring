package core

import (
	"context"
	"testing"
	"time"

	"pk95monitoring/internal/fake/fakegraph"
	"pk95monitoring/internal/model"
)

// A Microsoft 365 tenant is a device without host: polled every five minutes, judged by the
// thresholds, its app secrets and ending subscriptions listed under "Ablaufdaten".
func TestM365Device(t *testing.T) {
	e := newEnv(t, Options{})
	g := fakegraph.New()
	t.Cleanup(g.Close)
	e.c.M365.LoginURL, e.c.M365.GraphURL = g.URL(), g.URL()
	g.SetSyncAge(13 * time.Hour)

	_, errs, _ := e.c.SaveDevice(model.Device{Name: "x", Type: model.TypeM365, M365: &model.M365Config{TenantID: "kein mandant", ClientID: "abc"}})
	if errs["m365.tenant_id"] == "" || errs["m365.client_id"] == "" || errs["m365.secret"] == "" || errs["host"] != "" {
		t.Fatalf("validation %v", errs)
	}
	d, errs, err := e.c.SaveDevice(model.Device{Name: "Microsoft 365", Type: model.TypeM365, Enabled: true,
		M365: &model.M365Config{TenantID: " " + fakegraph.TenantID, ClientID: fakegraph.ClientID, Secret: fakegraph.Secret}})
	if err != nil || len(errs) > 0 {
		t.Fatalf("save: %v %v", errs, err)
	}
	if d.PollInterval() != 300 || d.M365.TenantID != fakegraph.TenantID {
		t.Fatalf("device %+v", d)
	}
	d.M365.Secret = "" // an edit without secret keeps it
	if d, errs, err = e.c.SaveDevice(d); err != nil || len(errs) > 0 || d.M365.Secret != fakegraph.Secret {
		t.Fatalf("edit: %v %v", errs, err)
	}
	st, err := e.c.PollNow(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	sync, _ := findCheck(st, "m365:sync")
	app, _ := findCheck(st, "m365:app:8c1d2e3f-4a5b-4c6d-8e9f-0a1b2c3d4e5f")
	expired, _ := findCheck(st, "m365:app:1f4a5b6c-7d8e-4f9a-1b2c-3d4e5f6a7b8c")
	if sync.State != model.StateCrit || app.State != model.StateWarn || expired.State != model.StateCrit || st.State != model.StateCrit {
		t.Errorf("states: sync %s, app %s, expired %s, device %s", sync.State, app.State, expired.State, st.State)
	}
	if _, ok := rowOf(e.c.Rows(), d.ID); !ok {
		t.Fatal("not in the device list")
	}
	kinds := map[string]int{}
	for _, r := range e.c.Expiry() {
		if r.DeviceID == d.ID {
			kinds[r.Kind]++
			if r.Until == "" || r.Days == nil {
				t.Errorf("expiry row %+v", r)
			}
		}
	}
	// apps: monitoring, scan-to-mail, Veeam, Teams connector; licenses: grace period, trial
	if kinds["app"] != 4 || kinds["license"] != 2 {
		t.Errorf("expiry kinds %v", kinds)
	}
	res, err := e.c.TestM365(context.Background(), d.ID, model.M365Config{TenantID: fakegraph.TenantID, ClientID: fakegraph.ClientID})
	if err != nil || res.Tenant != "Muster GmbH" {
		t.Errorf("test with the stored secret: %+v %v", res, err)
	}
}
