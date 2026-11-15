package snmp_test

import (
	"strings"
	"testing"

	"pk95monitoring/internal/fake/fakesnmp"
	"pk95monitoring/internal/model"
)

func TestSophosFirewall(t *testing.T) {
	fw := fakesnmp.NewSophos()
	c, clk := setup(map[string]*fakesnmp.Device{"fw": fw})
	fw.SetLicense(3, 3, clk.now().AddDate(0, 0, 20)) // the collector runs on the test clock
	d := device("fw", model.SNMPConfig{})
	m := collect(t, c, d)

	sys := mustCheck(t, m, "snmp:system")
	if sys.State != model.StateOK || sys.Facts["Profil"] != "Sophos Firewall" || sys.Facts["Modell"] != "XGS2100" ||
		!strings.HasPrefix(sys.Facts["Firmware"], "SFOS 21") || sys.Facts["Angemeldete Benutzer"] == "" {
		t.Errorf("system: %+v", sys)
	}
	if mem := mustCheck(t, m, "snmp:mem"); !strings.HasSuffix(mem.Message, "%)") {
		t.Errorf("mem: %+v", mem)
	} else if u, ok := metric(mem, "mem.usage"); !ok || u.Value < 50 || u.Value > 66 {
		t.Errorf("mem.usage: %+v", mem)
	}
	if st := mustCheck(t, m, "snmp:storage:Firewall"); st.State != model.StateOK {
		t.Errorf("storage: %+v", st)
	}
	mustCheck(t, m, "snmp:cpu")
	if ha := mustCheck(t, m, "snmp:ha"); ha.State != model.StateOK || ha.Message != "Primär, Partner Hilfsgerät (Auxiliary)" {
		t.Errorf("ha: %+v", ha)
	}
	if l := mustCheck(t, m, "snmp:lic:Base Firewall"); l.State != model.StateOK || l.Message != "unbefristet" || len(l.Metrics) != 0 {
		t.Errorf("base license: %+v", l)
	}
	web := mustCheck(t, m, "snmp:lic:Web Protection")
	if mt, ok := metric(web, "license.days"); !ok || mt.Value < 19 || mt.Value > 20 || !strings.HasPrefix(web.Message, "gültig bis ") {
		t.Errorf("web license: %+v", web)
	}
	if _, ok := m["snmp:lic:Email Protection"]; ok {
		t.Error("an unsubscribed module has a license check")
	}
	if v := mustCheck(t, m, "snmp:vpn:Filiale Nord"); v.State != model.StateOK || v.Message != "verbunden" || v.Facts["Typ"] != "Site-to-Site" {
		t.Errorf("vpn: %+v", v)
	}
	if v := mustCheck(t, m, "snmp:vpn:Homeoffice GF"); v.State != model.StateOK || v.Message != "nicht verbunden" {
		t.Errorf("host-to-host vpn: %+v", v)
	}
	if s := mustCheck(t, m, "snmp:services"); s.State != model.StateOK || s.Message != "15 Dienste laufen" {
		t.Errorf("services: %+v", s)
	}

	// problems: a tunnel down, one partially up, the HA peer gone, an expired license, a crashed service
	fw.SetTunnel("Filiale Nord", 0)
	fw.SetTunnel("Rechenzentrum", 2)
	fw.SetHAPeer(0)
	fw.SetLicense(2, 4, clk.now().AddDate(0, 0, -3))
	fw.SetService(17, 5)
	fw.SetService(11, 1)
	m = collect(t, c, d)
	want := map[string]string{"snmp:vpn:Filiale Nord": model.StateCrit, "snmp:vpn:Rechenzentrum": model.StateWarn,
		"snmp:ha": model.StateCrit, "snmp:lic:Network Protection": model.StateCrit, "snmp:services": model.StateCrit}
	for k, st := range want {
		if ck := mustCheck(t, m, k); ck.State != st {
			t.Errorf("%s: %s (%s), want %s", k, ck.State, ck.Message, st)
		}
	}
	if s := mustCheck(t, m, "snmp:services"); !strings.HasPrefix(s.Message, "Webadmin (Apache) gestoppt, Netzwerk abgestürzt") {
		t.Errorf("services message %q", s.Message)
	}
}
