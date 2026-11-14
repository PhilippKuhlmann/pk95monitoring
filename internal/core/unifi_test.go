package core

import (
	"context"
	"testing"

	"pk95monitoring/internal/fake/fakeunifi"
	"pk95monitoring/internal/model"
)

// A UniFi console is an ordinary device; its Protect cameras feed the template "Videoüberwachung".
func TestUniFiDevice(t *testing.T) {
	e := newEnv(t, Options{})
	srv := fakeunifi.New()
	t.Cleanup(srv.Close)
	cfg := srv.Config()
	_, errs, _ := e.c.SaveDevice(model.Device{Name: "x", Type: model.TypeUniFi, UniFi: &model.UniFiConfig{Fingerprint: "zz"}})
	if errs["host"] == "" || errs["unifi.username"] == "" || errs["unifi.password"] == "" || errs["unifi.fingerprint"] == "" {
		t.Fatalf("validation %v", errs)
	}
	d, errs, err := e.c.SaveDevice(model.Device{Name: "UDM", Type: model.TypeUniFi, Host: srv.Host(), Enabled: true, UniFi: &cfg})
	if err != nil || len(errs) > 0 {
		t.Fatalf("save: %v %v", errs, err)
	}
	d.UniFi.Password = "" // an edit without password keeps it
	if d, errs, err = e.c.SaveDevice(d); err != nil || len(errs) > 0 || d.UniFi.Password != fakeunifi.Password {
		t.Fatalf("edit: %v %v", errs, err)
	}
	st, err := e.c.PollNow(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != model.StateCrit {
		t.Errorf("state %s (an access point and a camera are offline)", st.State)
	}
	for _, k := range []string{"reach", "unifi:console", "unifi:wan:default", "unifi:nvr", "unifi:disk:1", "unifi:cam:cam0000000000000000000001"} {
		if _, ok := findCheck(st, k); !ok {
			t.Errorf("missing %s", k)
		}
	}
	p, _, errs, err := e.c.ApplyStatusTemplate(TplVideo, "")
	if err != nil || len(errs) > 0 || p.Components[0].Name != "Videoüberwachung" || p.Components[0].Devices[0] != d.ID {
		t.Fatalf("template: %v %v %+v", errs, err, p.Components)
	}
	pv, _ := e.c.StatusPreview(context.Background())
	// 5 cameras, the recording check and a disk; one camera is offline: partial
	if c0 := pv.Components[0]; c0.Units != 7 || c0.State != PubWarn {
		t.Errorf("video service %+v", c0)
	}
	if _, err := e.c.TestUniFi(context.Background(), d.ID, srv.Host(), model.UniFiConfig{Port: cfg.Port, Username: cfg.Username, Fingerprint: cfg.Fingerprint}); err != nil {
		t.Errorf("test with the stored password: %v", err)
	}
}
