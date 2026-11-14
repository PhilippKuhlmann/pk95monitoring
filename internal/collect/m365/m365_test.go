package m365

import (
	"context"
	"strings"
	"testing"
	"time"

	"pk95monitoring/internal/fake/fakegraph"
	"pk95monitoring/internal/model"
)

func newTest(t *testing.T) (*Collector, *fakegraph.Server, model.Device) {
	t.Helper()
	g := fakegraph.New()
	t.Cleanup(g.Close)
	c := New()
	c.LoginURL, c.GraphURL = g.URL(), g.URL()
	d := model.Device{ID: "m1", Type: model.TypeM365, M365: &model.M365Config{TenantID: fakegraph.TenantID, ClientID: fakegraph.ClientID, Secret: fakegraph.Secret}}
	return c, g, d
}

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

func mustCollect(t *testing.T, c *Collector, d model.Device) []model.Check {
	t.Helper()
	cs, err := c.Collect(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func TestCollect(t *testing.T) {
	c, g, d := newTest(t)
	m := byKey(mustCollect(t, c, d))
	if tn := m["m365:tenant"]; tn.State != model.StateWarn || !strings.HasPrefix(tn.Message, "muster-shop.de nicht verifiziert · Muster GmbH · muster-gmbh.de · 3 Domains") ||
		tn.Facts["Mandanten-ID"] != fakegraph.TenantID {
		t.Errorf("tenant %+v", tn)
	}
	if s := m["m365:sync"]; s.State != model.StateOK || s.Message != "letzte Synchronisierung vor 18 Min." || s.Facts["Letzte Kennwort-Synchronisierung"] == "" {
		t.Errorf("sync %+v", s)
	} else if v, _ := metric(s, "m365.sync_age"); v != 0.3 {
		t.Errorf("sync age %v", v)
	}
	if s := m["m365:sku:spb"]; s.Name != "Microsoft 365 Business Premium" || s.State != model.StateOK || s.Message != "23 von 25 zugewiesen · 2 frei" {
		t.Errorf("SPB %+v", s)
	} else if _, ok := metric(s, "m365.sub_days"); ok {
		t.Error("a renewing subscription has a countdown")
	}
	if s := m["m365:sku:aad_premium"]; s.State != model.StateWarn || !strings.Contains(s.Message, "Kulanzzeit") {
		t.Errorf("grace period %+v", s)
	} else if v, _ := metric(s, "m365.sub_days"); v < 21.9 || v > 22.1 {
		t.Errorf("grace days %v", v)
	}
	if s := m["m365:sku:microsoft_365_copilot"]; !strings.HasPrefix(s.Message, "Testversion bis ") || s.Facts["Testversion"] != "ja" {
		t.Errorf("trial %+v", s)
	}
	if _, ok := m["m365:sku:flow_free"]; ok {
		t.Error("free self-service SKU listed")
	}
	if self := m["m365:app:"+fakegraph.ClientID]; self.Name != "PK95 Monitoring" || self.Facts["Hinweis"] == "" || self.State != model.StateOK {
		t.Errorf("own app %+v", self)
	}
	if s := m["m365:app:8c1d2e3f-4a5b-4c6d-8e9f-0a1b2c3d4e5f"]; !strings.HasPrefix(s.Message, "Geheimnis „smtp“ gültig bis ") || !strings.HasSuffix(s.Message, "(noch 12 Tage)") {
		t.Errorf("expiring secret %+v", s)
	} else if v, _ := metric(s, "m365.cred_days"); v < 12 || v > 12.3 {
		t.Errorf("cred days %v", v)
	}
	if s := m["m365:app:9d2e3f4a-5b6c-4d7e-9f0a-1b2c3d4e5f6a"]; !strings.HasPrefix(s.Message, "Zertifikat „CN=VBO365“") {
		t.Errorf("certificate %+v", s)
	}
	// expired 3 days ago: judged by the newest credential; long expired and without: left out
	if s := m["m365:app:1f4a5b6c-7d8e-4f9a-1b2c-3d4e5f6a7b8c"]; !strings.Contains(s.Message, "„2025“ abgelaufen am") {
		t.Errorf("expired %+v", s)
	} else if v, _ := metric(s, "m365.cred_days"); v > -2.9 || v < -3.1 {
		t.Errorf("expired days %v", v)
	}
	for _, k := range []string{"m365:app:0e3f4a5b-6c7d-4e8f-0a1b-2c3d4e5f6a7b", "m365:app:2a5b6c7d-8e9f-4a0b-2c3d-4e5f6a7b8c9d"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s listed", k)
		}
	}
	if h := m["m365:health"]; h.State != model.StateWarn || h.Message != "Exchange Online eingeschränkt (EX982341)" ||
		!strings.HasPrefix(h.Facts["Exchange Online"], "EX982341: Eingehende") || h.Facts["Hinweise von Microsoft"] != "1" {
		t.Errorf("health %+v", h)
	}
	if p := m["m365:perm"]; p.State != model.StateOK {
		t.Errorf("permissions %+v", p)
	}

	// the incident is over; Entra Connect stopped a day ago (judged by the threshold)
	g.SetIncident(false)
	g.SetSyncAge(26 * time.Hour)
	m = byKey(mustCollect(t, c, d))
	if h := m["m365:health"]; h.State != model.StateOK || h.Message != "Alle 8 Dienste in Betrieb · 1 Hinweis von Microsoft" {
		t.Errorf("health ok %+v", h)
	}
	if v, _ := metric(m["m365:sync"], "m365.sync_age"); v != 26 || m["m365:sync"].Message != "letzte Synchronisierung vor 26 Std." {
		t.Errorf("old sync %+v", m["m365:sync"])
	}
}

func TestPermissions(t *testing.T) {
	c, g, d := newTest(t)
	g.Deny("/v1.0/applications")
	g.Deny("/v1.0/admin/")
	m := byKey(mustCollect(t, c, d))
	p := m["m365:perm"]
	if p.State != model.StateWarn || !strings.Contains(p.Message, "App-Registrierungen (Application.Read.All)") || !strings.Contains(p.Message, "Dienststatus (ServiceHealth.Read.All)") {
		t.Errorf("permissions %+v", p)
	}
	if _, ok := m["m365:health"]; ok {
		t.Error("health without permission")
	}
	for k := range m {
		if strings.HasPrefix(k, "m365:app:") {
			t.Errorf("app without permission: %s", k)
		}
	}
	res, err := c.Test(context.Background(), *d.M365)
	if err != nil || res.Tenant != "Muster GmbH" || res.Domain != "muster-gmbh.de" || !res.Sync || res.Licenses != 4 ||
		strings.Join(res.Missing, ",") != "Application.Read.All,ServiceHealth.Read.All" {
		t.Errorf("test %+v %v", res, err)
	}
	g.Deny("/v1.0/organization")
	c.Forget(d.ID)
	if _, err := c.Collect(context.Background(), d); err == nil || !strings.Contains(err.Error(), "Organization.Read.All") {
		t.Errorf("tenant without permission: %v", err)
	}
}

func TestSignIn(t *testing.T) {
	c, _, d := newTest(t)
	for _, tc := range []struct {
		cfg  model.M365Config
		want string
	}{
		{model.M365Config{TenantID: "00000000-0000-0000-0000-000000000000", ClientID: fakegraph.ClientID, Secret: fakegraph.Secret}, "Mandant nicht gefunden"},
		{model.M365Config{TenantID: fakegraph.TenantID, ClientID: "00000000-0000-0000-0000-000000000001", Secret: fakegraph.Secret}, "App nicht gefunden"},
		{model.M365Config{TenantID: fakegraph.TenantID, ClientID: fakegraph.ClientID, Secret: "falsch"}, "Geheimer Schlüssel ungültig"},
	} {
		dd := d
		dd.M365 = &tc.cfg
		if _, err := c.Collect(context.Background(), dd); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v", tc.cfg, err)
		}
	}
	// a domain works as tenant; the apps are read over two pages
	res, err := c.Test(context.Background(), model.M365Config{TenantID: "mustergmbh.onmicrosoft.com", ClientID: fakegraph.ClientID, Secret: fakegraph.Secret})
	if err != nil || res.Apps != 4 || len(res.Missing) != 0 {
		t.Errorf("test by domain %+v %v", res, err)
	}
	if _, err := c.Collect(context.Background(), model.Device{ID: "x", M365: &model.M365Config{}}); err == nil {
		t.Error("collect without credentials")
	}
}

func TestSkuName(t *testing.T) {
	if SkuName("SPB") != "Microsoft 365 Business Premium" || SkuName("SOMETHING_NEW") != "SOMETHING_NEW" {
		t.Error("sku names")
	}
}
