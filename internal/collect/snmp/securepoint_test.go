package snmp_test

import (
	"strings"
	"testing"
	"time"

	"pk95monitoring/internal/fake/fakesnmp"
	"pk95monitoring/internal/model"
)

func TestSecurepointUTM(t *testing.T) {
	utm := fakesnmp.NewSecurepoint()
	c, clk := setup(map[string]*fakesnmp.Device{"utm": utm})
	utm.SetClock(clk.now) // the WireGuard handshake timestamps follow the test clock
	d := device("utm", model.SNMPConfig{})
	m := collect(t, c, d)

	sys := mustCheck(t, m, "snmp:system")
	if sys.State != model.StateOK || sys.Facts["Profil"] != "Securepoint UTM" || sys.Facts["Firmware"] != "14.1.9.3" || sys.Facts["Last"] == "" {
		t.Errorf("system: %+v", sys)
	}
	if cpu := mustCheck(t, m, "snmp:cpu"); cpu.Metrics[0].Value < 5 || cpu.Metrics[0].Value > 25 {
		t.Errorf("cpu: %+v", cpu)
	}
	if mem := mustCheck(t, m, "snmp:mem"); !strings.HasSuffix(mem.Message, "%)") {
		t.Errorf("mem: %+v", mem)
	}
	lic := mustCheck(t, m, "snmp:lic:UTM")
	if mt, ok := metric(lic, "license.days"); !ok || mt.Value != 120 || lic.Facts["Lizenznehmer"] != "Muster GmbH" || !strings.HasPrefix(lic.Message, "gültig bis ") {
		t.Errorf("license: %+v", lic)
	}
	if cl := mustCheck(t, m, "snmp:cluster"); cl.State != model.StateOK || cl.Message != "Master · synchronisiert" || cl.Facts["Priorität"] != "hoch" {
		t.Errorf("cluster: %+v", cl)
	}
	if v := mustCheck(t, m, "snmp:vpn:Zentrale-Lager"); v.State != model.StateOK || v.Message != "verbunden" {
		t.Errorf("ipsec: %+v", v)
	}
	if s := mustCheck(t, m, "snmp:ipsec-subnets"); s.State != model.StateOK || s.Message != "3 von 3 Subnetzpaaren verbunden" {
		t.Errorf("subnets: %+v", s)
	}
	if w := mustCheck(t, m, "snmp:wg:Lager-Kasse"); w.State != model.StateOK || w.Message != "verbunden · Handshake vor 35 s" || w.Facts["Gegenstelle"] != "91.40.12.7:51820" {
		t.Errorf("wireguard: %+v", w)
	}
	if w := mustCheck(t, m, "snmp:wg:Notebook-Chef"); w.State != model.StateOK || w.Message != "noch nie verbunden" {
		t.Errorf("wireguard never: %+v", w)
	}
	if s := mustCheck(t, m, "snmp:services"); s.State != model.StateOK || s.Message != "8 Dienste laufen" || s.Facts["ikarus"] != "läuft" || s.Facts["openvpn"] != "" {
		t.Errorf("services: %+v", s)
	}
	if a := mustCheck(t, m, "snmp:av"); a.State != model.StateOK || !strings.HasPrefix(a.Facts["ikarus"], "aktuell · Stand ") {
		t.Errorf("av: %+v", a)
	}
	if ct := mustCheck(t, m, "snmp:conntrack"); !strings.Contains(ct.Message, "von 65536 Verbindungen") {
		t.Errorf("conntrack: %+v", ct)
	}

	// problems
	utm.SetSPTunnel("Zentrale-Lager", 2)
	utm.SetSPSubnetDown(1, true)
	utm.SetSPHandshakeAgo("Lager-Kasse", 3600)
	utm.SetSPCluster("backup", "error")
	utm.SetSPAppError("named", true)
	utm.SetSPAVFailed(true)
	utm.SetSPLicenseDays(0)
	clk.advance(time.Minute)
	m = collect(t, c, d)
	want := map[string]string{"snmp:vpn:Zentrale-Lager": model.StateCrit, "snmp:ipsec-subnets": model.StateCrit, "snmp:wg:Lager-Kasse": model.StateWarn,
		"snmp:cluster": model.StateCrit, "snmp:services": model.StateWarn, "snmp:av": model.StateWarn, "snmp:lic:UTM": model.StateCrit}
	for k, st := range want {
		if ck := mustCheck(t, m, k); ck.State != st {
			t.Errorf("%s: %s (%s), want %s", k, ck.State, ck.Message, st)
		}
	}
	if s := m["snmp:ipsec-subnets"]; s.Message != "getrennt: 10.20.0.0/24 ↔ 10.31.0.0/24 · 2 von 3 Subnetzpaaren verbunden" {
		t.Errorf("subnets message %q", s.Message)
	}
	// no Ikarus license (as on a real UTM 14.1): ikarus error(4) and a pattern row that never
	// loaded are no problem
	utm.SetSPAppError("named", false)
	utm.SetSPAVFailed(false)
	utm.SetSPAVUnlicensed(true)
	m = collect(t, c, d)
	if s := m["snmp:services"]; s.State != model.StateOK || s.Facts["ikarus"] != "nicht lizenziert" || s.Message != "7 Dienste laufen" {
		t.Errorf("unlicensed ikarus: %+v", s)
	}
	if a, ok := m["snmp:av"]; ok {
		t.Errorf("av without a scanner in use: %+v", a)
	}

	utm.SetSPLicenseDays(-1) // 0xffffffff: unlimited
	if m = collect(t, c, d); m["snmp:lic:UTM"].Message != "unbefristet" {
		t.Errorf("unlimited license: %+v", m["snmp:lic:UTM"])
	}
}
