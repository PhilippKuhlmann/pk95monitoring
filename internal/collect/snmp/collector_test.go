package snmp_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"pk95monitoring/internal/collect/snmp"
	"pk95monitoring/internal/fake/fakesnmp"
	"pk95monitoring/internal/model"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

// setup wires the devices (by host name) into a collector with a shared fake clock.
func setup(devs map[string]*fakesnmp.Device) (*snmp.Collector, *clock) {
	clk := &clock{t: time.Unix(1_750_000_000, 0)}
	for _, d := range devs {
		d.SetClock(clk.now)
	}
	return &snmp.Collector{Dial: fakesnmp.Dialer(devs), Timeout: time.Second, Now: clk.now}, clk
}

func device(host string, cfg model.SNMPConfig) model.Device {
	return model.Device{ID: "dev-" + host, Name: host, Type: model.TypeSNMP, Host: host, Enabled: true, SNMP: &cfg}
}

func collect(t *testing.T, c *snmp.Collector, d model.Device) map[string]model.Check {
	t.Helper()
	checks, err := c.Collect(context.Background(), d)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	m := map[string]model.Check{}
	for _, ck := range checks {
		if _, dup := m[ck.Key]; dup {
			t.Errorf("duplicate check key %q", ck.Key)
		}
		m[ck.Key] = ck
	}
	return m
}

func mustCheck(t *testing.T, m map[string]model.Check, key string) model.Check {
	t.Helper()
	ck, ok := m[key]
	if !ok {
		var keys []string
		for k := range m {
			keys = append(keys, k)
		}
		t.Fatalf("check %q missing; have %v", key, keys)
	}
	return ck
}

func metric(ck model.Check, name string) (model.Metric, bool) {
	for _, m := range ck.Metrics {
		if m.Name == name {
			return m, true
		}
	}
	return model.Metric{}, false
}

func TestSwitchInterfacesAndRates(t *testing.T) {
	sw := fakesnmp.NewSwitch(24)
	c, clk := setup(map[string]*fakesnmp.Device{"sw": sw})
	d := device("sw", model.SNMPConfig{})

	first := collect(t, c, d)
	sys := mustCheck(t, first, "snmp:system")
	if sys.State != model.StateOK || sys.Facts["Name"] != "sw-core" || sys.Facts["Profil"] != "Switch" ||
		sys.Facts["Standort"] != "Serverraum" || sys.Facts["Beschreibung"] == "" {
		t.Errorf("system check: %+v", sys)
	}
	if up, ok := metric(sys, "uptime"); !ok || up.Unit != "s" || up.Value < 86400 {
		t.Errorf("uptime metric: %+v", sys.Metrics)
	}
	ifCount := 0
	for k, ck := range first {
		if strings.HasPrefix(k, "snmp:if:") {
			ifCount++
			if len(ck.Metrics) != 0 {
				t.Errorf("%s: first poll must not have rates: %+v", k, ck.Metrics)
			}
			if ck.State != model.StateOK || ck.Group != "Schnittstellen" {
				t.Errorf("%s: %+v", k, ck)
			}
		}
	}
	// every port is reported: Gi1/0/1…24 (19…23 without link, 24 disabled), two uplinks
	// and vlan1; the loopback is skipped. Ports without link are ok.
	if ifCount != 27 {
		t.Errorf("got %d interface checks, want 27", ifCount)
	}
	if _, ok := first["snmp:if:lo"]; ok {
		t.Error("loopback must be skipped")
	}
	if ck := mustCheck(t, first, "snmp:if:Gi1/0/20"); ck.Message != "Kein Link" || ck.Facts["Status"] != "getrennt" ||
		ck.Facts["Admin"] != "" || ck.Facts["Alarm bei Link-Ausfall"] != "" {
		t.Errorf("unwatched port without link: %+v", ck)
	}
	if ck := mustCheck(t, first, "snmp:if:Gi1/0/24"); ck.Message != "Deaktiviert" || ck.Facts["Admin"] != "deaktiviert" {
		t.Errorf("disabled port: %+v", ck)
	}
	if ck := mustCheck(t, first, "snmp:if:Te1/1/1"); ck.Message != "Verbunden mit 10 Gbit/s (Uplink Kern-Switch)" ||
		ck.Facts["Status"] != "verbunden" {
		t.Errorf("uplink Te1/1/1: %+v", ck)
	}
	if ck := mustCheck(t, first, "snmp:if:Gi1/0/1"); ck.Name != "Port Gi1/0/1" || ck.Facts["Alias"] != "Uplink Router" ||
		!strings.Contains(ck.Message, "1 Gbit/s") {
		t.Errorf("uplink: %+v", ck)
	}
	if cpu := mustCheck(t, first, "snmp:cpu"); len(cpu.Metrics) != 1 || cpu.Metrics[0].Name != "cpu.usage" {
		t.Errorf("cpu: %+v", cpu)
	}

	clk.advance(60 * time.Second)
	second := collect(t, c, d)
	up := mustCheck(t, second, "snmp:if:Gi1/0/1")
	rx, ok := metric(up, "if.rx")
	if !ok || rx.Unit != "B/s" || rx.Instance != "Gi1/0/1" || rx.Value < 0.4*12e6 || rx.Value > 1.6*12e6 {
		t.Errorf("if.rx = %+v (ok %v)", rx, ok)
	}
	if tx, ok := metric(up, "if.tx"); !ok || tx.Value <= 0 {
		t.Errorf("if.tx = %+v", tx)
	}
	if e, ok := metric(up, "if.errors"); !ok || e.Value != 0 || e.Unit != "1/s" {
		t.Errorf("if.errors on port 1 = %+v", e)
	}
	if e, ok := metric(mustCheck(t, second, "snmp:if:Gi1/0/3"), "if.errors"); !ok || e.Value < 0.1 || e.Value > 0.3 {
		t.Errorf("if.errors on port 3 = %+v", e)
	}
	for _, k := range []string{"snmp:if:Gi1/0/20", "snmp:if:Gi1/0/24"} {
		if ck := mustCheck(t, second, k); len(ck.Metrics) != 0 {
			t.Errorf("%s: no traffic metrics while down: %+v", k, ck.Metrics)
		}
	}
	if _, ok := metric(mustCheck(t, second, "snmp:if:Te1/1/2"), "if.rx"); !ok {
		t.Error("uplink Te1/1/2 has no if.rx")
	}
}

func TestWatchedPortDown(t *testing.T) {
	sw := fakesnmp.NewSwitch(24)
	c, _ := setup(map[string]*fakesnmp.Device{"sw": sw})
	d := device("sw", model.SNMPConfig{Interfaces: []string{"gi1/0/2", "GigabitEthernet1/0/20", "Gi9/9/9", "Gi1/0/24"}})

	sw.SetPortUp(2, false)
	m := collect(t, c, d)
	ck := mustCheck(t, m, "snmp:if:Gi1/0/2")
	if ck.State != model.StateCrit || ck.Message != "Kein Link" || ck.Facts["Alarm bei Link-Ausfall"] != "ja" ||
		ck.Facts["Status"] != "getrennt" || len(ck.Metrics) != 0 {
		t.Errorf("watched port 2: %+v", ck)
	}
	// matched by ifDescr, key still uses ifName
	if ck := mustCheck(t, m, "snmp:if:Gi1/0/20"); ck.State != model.StateCrit || ck.Message != "Kein Link" {
		t.Errorf("watched port 20: %+v", ck)
	}
	if ck := mustCheck(t, m, "snmp:if:Gi1/0/24"); ck.State != model.StateCrit || ck.Message != "Port deaktiviert" ||
		ck.Facts["Admin"] != "deaktiviert" {
		t.Errorf("watched disabled port 24: %+v", ck)
	}
	// unwatched ports without link stay ok
	if ck := mustCheck(t, m, "snmp:if:Gi1/0/21"); ck.State != model.StateOK || ck.Message != "Kein Link" {
		t.Errorf("unwatched port 21: %+v", ck)
	}
	if ck := mustCheck(t, m, "snmp:if:Gi9/9/9"); ck.State != model.StateUnknown {
		t.Errorf("missing watched port: %+v", ck)
	}

	sw.SetPortUp(2, true)
	m = collect(t, c, d)
	if ck := mustCheck(t, m, "snmp:if:Gi1/0/2"); ck.State != model.StateOK {
		t.Errorf("port 2 back up: %+v", ck)
	}
}

func TestCounters32BitWrapAndReboot(t *testing.T) {
	sw := fakesnmp.NewSwitch(8)
	sw.Hide("1.3.6.1.2.1.31.1.1.1.6") // no ifHCInOctets: 32-bit counters only
	c, clk := setup(map[string]*fakesnmp.Device{"sw": sw})
	d := device("sw", model.SNMPConfig{})
	const inOctets1 = "1.3.6.1.2.1.2.2.1.10.1"

	sw.Set(inOctets1, gosnmp.Counter32, uint(4294967000))
	collect(t, c, d)
	clk.advance(10 * time.Second)
	sw.Set(inOctets1, gosnmp.Counter32, uint(1000))
	m := collect(t, c, d)
	rx, ok := metric(mustCheck(t, m, "snmp:if:Gi1/0/1"), "if.rx")
	if !ok || rx.Value < 129.5 || rx.Value > 129.7 {
		t.Errorf("wrapped if.rx = %+v (ok %v), want 129.6", rx, ok)
	}

	// a reboot (uptime goes back) invalidates all counters
	clk.advance(10 * time.Second)
	sw.Set("1.3.6.1.2.1.1.3.0", gosnmp.TimeTicks, uint32(500))
	sw.Set(inOctets1, gosnmp.Counter32, uint(2000))
	m = collect(t, c, d)
	if _, ok := metric(mustCheck(t, m, "snmp:if:Gi1/0/1"), "if.rx"); ok {
		t.Error("no rate expected after a reboot")
	}
	clk.advance(10 * time.Second)
	sw.Set("1.3.6.1.2.1.1.3.0", gosnmp.TimeTicks, uint32(1500))
	sw.Set(inOctets1, gosnmp.Counter32, uint(12000))
	m = collect(t, c, d)
	if rx, ok := metric(mustCheck(t, m, "snmp:if:Gi1/0/1"), "if.rx"); !ok || rx.Value != 1000 {
		t.Errorf("rate after reboot = %+v (ok %v)", rx, ok)
	}
}

func TestUPS(t *testing.T) {
	ups := fakesnmp.NewUPS()
	c, clk := setup(map[string]*fakesnmp.Device{"ups": ups})
	d := device("ups", model.SNMPConfig{})

	m := collect(t, c, d)
	ck := mustCheck(t, m, "snmp:ups")
	if ck.State != model.StateOK || !strings.HasPrefix(ck.Message, "Netzbetrieb") || ck.Name != "USV" {
		t.Errorf("ups on mains: %+v", ck)
	}
	want := map[string]string{"ups.battery": "%", "ups.runtime": "min", "ups.load": "%", "ups.input": "V", "ups.output": "V", "temp": "°C"}
	for name, unit := range want {
		if mt, ok := metric(ck, name); !ok || mt.Unit != unit {
			t.Errorf("metric %s: %+v (ok %v)", name, mt, ok)
		}
	}
	if b, _ := metric(ck, "ups.battery"); b.Value != 100 {
		t.Errorf("battery = %v", b.Value)
	}
	if in, _ := metric(ck, "ups.input"); in.Value < 220 || in.Value > 240 {
		t.Errorf("input = %v", in.Value)
	}
	if sys := mustCheck(t, m, "snmp:system"); sys.Facts["Profil"] != "USV" || sys.Facts["Modell"] == "" {
		t.Errorf("system facts: %+v", sys.Facts)
	}
	if _, ok := m["snmp:mem"]; ok {
		t.Error("no HOST-RESOURCES checks expected for a UPS")
	}

	ups.SetOnBattery(true)
	clk.advance(10 * time.Minute)
	ck = mustCheck(t, collect(t, c, d), "snmp:ups")
	if ck.State != model.StateWarn || !strings.Contains(ck.Message, "Läuft auf Batterie") {
		t.Errorf("on battery: %+v", ck)
	}
	if b, _ := metric(ck, "ups.battery"); b.Value != 85 {
		t.Errorf("battery after 10 min = %v, want 85", b.Value)
	}
	if in, _ := metric(ck, "ups.input"); in.Value != 0 {
		t.Errorf("input on battery = %v", in.Value)
	}

	ups.SetBatteryLow(true)
	ck = mustCheck(t, collect(t, c, d), "snmp:ups")
	if ck.State != model.StateCrit || !strings.Contains(ck.Message, "Batterie schwach") {
		t.Errorf("battery low: %+v", ck)
	}

	ups.SetBatteryLow(false)
	ups.SetOnBattery(false)
	ups.SetReplaceBattery(true)
	ck = mustCheck(t, collect(t, c, d), "snmp:ups")
	if ck.State != model.StateWarn || !strings.Contains(ck.Message, "getauscht") {
		t.Errorf("replace battery: %+v", ck)
	}
}

func TestUPSRFC1628Only(t *testing.T) {
	ups := fakesnmp.NewUPS()
	ups.Hide("1.3.6.1.4.1.318")
	ups.Set("1.3.6.1.2.1.1.2.0", gosnmp.ObjectIdentifier, ".1.3.6.1.4.1.32767.9")
	c, _ := setup(map[string]*fakesnmp.Device{"ups": ups})
	d := device("ups", model.SNMPConfig{})

	ups.SetOnBattery(true)
	m := collect(t, c, d)
	ck := mustCheck(t, m, "snmp:ups")
	if ck.State != model.StateWarn || !strings.Contains(ck.Message, "Läuft auf Batterie") {
		t.Errorf("RFC 1628 on battery: %+v", ck)
	}
	if _, ok := metric(ck, "ups.runtime"); !ok {
		t.Error("runtime from upsEstimatedMinutesRemaining missing")
	}
	if ck.Facts["Hersteller"] != "APC" {
		t.Errorf("facts: %+v", ck.Facts)
	}
}

func TestPrinter(t *testing.T) {
	prn := fakesnmp.NewPrinter()
	c, _ := setup(map[string]*fakesnmp.Device{"prn": prn})
	d := device("prn", model.SNMPConfig{})

	m := collect(t, c, d)
	levels := map[string]float64{"Black Toner": 64, "Cyan Toner": 45, "Magenta Toner": 7, "Yellow Toner": 72}
	for name, want := range levels {
		ck := mustCheck(t, m, "snmp:toner:"+name)
		mt, ok := metric(ck, "printer.toner")
		if !ok || mt.Value != want || mt.Instance != name || mt.Unit != "%" || ck.Name != name {
			t.Errorf("%s: %+v", name, ck)
		}
	}
	waste := mustCheck(t, m, "snmp:toner:Waste Toner Box")
	if waste.State != model.StateOK || waste.Message != "Noch vorhanden" || len(waste.Metrics) != 0 {
		t.Errorf("waste toner: %+v", waste)
	}
	prt := mustCheck(t, m, "snmp:printer")
	if prt.State != model.StateWarn || !strings.Contains(prt.Message, "Toner fast leer") {
		t.Errorf("printer: %+v", prt)
	}
	if p, ok := metric(prt, "printer.pages"); !ok || p.Value < 15000 {
		t.Errorf("pages: %+v", p)
	}
	if sys := mustCheck(t, m, "snmp:system"); sys.Facts["Profil"] != "Drucker" || sys.Facts["Seriennummer"] != "DEMO-PRN-4711" {
		t.Errorf("system facts: %+v", sys.Facts)
	}
	if _, ok := m["snmp:mem"]; ok {
		t.Error("printers must not report memory")
	}

	prn.SetTonerLevel("cyan", -2)
	prn.SetPaperOut(true)
	prn.SetJam(true)
	m = collect(t, c, d)
	if ck := mustCheck(t, m, "snmp:toner:Cyan Toner"); ck.State != model.StateUnknown || len(ck.Metrics) != 0 {
		t.Errorf("unknown level: %+v", ck)
	}
	prt = mustCheck(t, m, "snmp:printer")
	if prt.State != model.StateCrit || !strings.Contains(prt.Message, "Kein Papier") || !strings.Contains(prt.Message, "Papierstau") {
		t.Errorf("printer errors: %+v", prt)
	}
}

func TestNAS(t *testing.T) {
	nas := fakesnmp.NewNAS()
	c, _ := setup(map[string]*fakesnmp.Device{"nas": nas})
	d := device("nas", model.SNMPConfig{})

	m := collect(t, c, d)
	sys := mustCheck(t, m, "snmp:system")
	if sys.State != model.StateOK || sys.Facts["Profil"] != "NAS" || sys.Facts["Modell"] != "DS920+ (Demo)" ||
		sys.Facts["DSM-Version"] == "" || sys.Facts["Seriennummer"] == "" {
		t.Errorf("system: %+v", sys)
	}
	temp := mustCheck(t, m, "snmp:temp:System")
	if mt, ok := metric(temp, "temp"); !ok || mt.Instance != "System" || mt.Value < 30 || mt.Value > 50 {
		t.Errorf("temp: %+v", temp)
	}
	for _, k := range []string{"snmp:disk:Disk 1", "snmp:disk:Disk 4", "snmp:raid:Volume 1", "snmp:raid:Storage Pool 1"} {
		if ck := mustCheck(t, m, k); ck.State != model.StateOK {
			t.Errorf("%s: %+v", k, ck)
		}
	}
	vol := mustCheck(t, m, "snmp:storage:/volume1")
	total, _ := metric(vol, "disk.total")
	usage, _ := metric(vol, "disk.usage")
	if total.Value < 10.4e12 || total.Value > 10.6e12 || usage.Value < 60 || usage.Value > 64 || usage.Instance != "/volume1" {
		t.Errorf("volume (Integer32 overflow): total %v usage %+v", total.Value, usage)
	}
	if _, ok := m["snmp:storage:/run"]; ok {
		t.Error("pseudo file systems must be skipped")
	}
	mem, _ := metric(mustCheck(t, m, "snmp:mem"), "mem.usage")
	if mem.Value < 30 || mem.Value > 45 { // used minus buffers and cache
		t.Errorf("mem.usage = %v", mem.Value)
	}
	if cpu, _ := metric(mustCheck(t, m, "snmp:cpu"), "cpu.usage"); cpu.Value < 0 || cpu.Value > 40 {
		t.Errorf("cpu.usage = %v", cpu.Value)
	}

	nas.SetDiskStatus(2, 5)
	nas.SetRaidStatus(11)
	nas.SetSystemFailed(true)
	m = collect(t, c, d)
	if ck := mustCheck(t, m, "snmp:disk:Disk 2"); ck.State != model.StateCrit {
		t.Errorf("crashed disk: %+v", ck)
	}
	if ck := mustCheck(t, m, "snmp:raid:Volume 1"); ck.State != model.StateCrit || !strings.Contains(ck.Message, "degraded") {
		t.Errorf("degraded raid: %+v", ck)
	}
	if ck := mustCheck(t, m, "snmp:system"); ck.State != model.StateCrit {
		t.Errorf("failed system: %+v", ck)
	}
}

func TestProfileDetectionAndTest(t *testing.T) {
	devs := map[string]*fakesnmp.Device{
		"sw24": fakesnmp.NewSwitch(24),
		"sw4":  fakesnmp.NewSwitch(4),
		"ups":  fakesnmp.NewUPS(),
		"prn":  fakesnmp.NewPrinter(),
		"nas":  fakesnmp.NewNAS(),
	}
	c, _ := setup(devs)
	tests := []struct {
		host, profile, sysName string
		ifs                    int
	}{
		{"sw24", snmp.ProfileSwitch, "sw-core", 27},
		{"sw4", snmp.ProfileGeneric, "sw-core", 5},
		{"ups", snmp.ProfileUPS, "usv-01", 1},
		{"prn", snmp.ProfilePrinter, "drucker-buero", 1},
		{"nas", snmp.ProfileNAS, "nas01", 2},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			res, err := c.Test(context.Background(), tt.host, model.SNMPConfig{Profile: "auto"})
			if err != nil {
				t.Fatal(err)
			}
			if res.Profile != tt.profile || res.SysName != tt.sysName || res.SysDescr == "" || len(res.Interfaces) != tt.ifs {
				t.Errorf("got %+v", res)
			}
		})
	}

	res, err := c.Test(context.Background(), "sw24", model.SNMPConfig{Profile: "printer"})
	if err != nil || res.Profile != snmp.ProfilePrinter {
		t.Errorf("configured profile must win: %+v %v", res, err)
	}
	if res.Interfaces[19].Oper != "down" || res.Interfaces[0].Oper != "up" || res.Interfaces[0].Speed != 1e9 ||
		res.Interfaces[23].Oper != "down" || res.Interfaces[24].Name != "Te1/1/1" || res.Interfaces[24].Speed != 10e9 {
		t.Errorf("interfaces: %+v", res.Interfaces)
	}
	b, _ := json.Marshal(res)
	for _, key := range []string{`"sys_name"`, `"sys_descr"`, `"profile"`, `"interfaces"`, `"name"`, `"descr"`, `"alias"`, `"speed"`, `"oper"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("JSON lacks %s: %s", key, b[:120])
		}
	}
}

func TestForcedProfileWithoutMIB(t *testing.T) {
	sw := fakesnmp.NewSwitch(2)
	sw.Hide("1.3.6.1.2.1.2")
	sw.Hide("1.3.6.1.2.1.31")
	sw.Hide("1.3.6.1.2.1.25")
	c, _ := setup(map[string]*fakesnmp.Device{"sw": sw})

	m := collect(t, c, device("sw", model.SNMPConfig{Profile: "generic"}))
	if len(m) != 1 {
		t.Errorf("only snmp:system expected, got %d checks", len(m))
	}
	mustCheck(t, m, "snmp:system")

	for profile, key := range map[string]string{"ups": "snmp:ups", "printer": "snmp:printer"} {
		m = collect(t, c, device("sw", model.SNMPConfig{Profile: profile}))
		if ck := mustCheck(t, m, key); ck.State != model.StateUnknown {
			t.Errorf("%s without MIB: %+v", profile, ck)
		}
	}
	m = collect(t, c, device("sw", model.SNMPConfig{Profile: "nas"}))
	if ck := mustCheck(t, m, "snmp:system"); ck.State != model.StateUnknown || !strings.Contains(ck.Message, "Synology") {
		t.Errorf("nas without MIB: %+v", ck)
	}
}

func TestCustomOIDs(t *testing.T) {
	sw := fakesnmp.NewSwitch(4)
	sw.Set("1.3.6.1.4.1.9999.1.0", gosnmp.Integer, 235)
	sw.Set("1.3.6.1.4.1.9999.3.0", gosnmp.OctetString, "Alles gut")
	sw.Set("1.3.6.1.4.1.9999.4.0", gosnmp.OctetString, "41.5")
	c, _ := setup(map[string]*fakesnmp.Device{"sw": sw})
	d := device("sw", model.SNMPConfig{OIDs: []model.CustomOID{
		{OID: ".1.3.6.1.4.1.9999.1.0", Name: "Gehäuse", Unit: "°C", Scale: 0.1},
		{OID: "1.3.6.1.4.1.9999.2.0", Name: "Fehlt"},
		{OID: "1.3.6.1.4.1.9999.3.0", Name: "Text"},
		{OID: "1.3.6.1.4.1.9999.4.0", Name: "Zahl als Text", Unit: "%"},
		{OID: "1.3.x", Name: "Kaputt"},
	}})

	m := collect(t, c, d)
	ck := mustCheck(t, m, "snmp:custom:Gehäuse")
	mt, ok := metric(ck, "custom")
	if !ok || mt.Value < 23.49 || mt.Value > 23.51 || mt.Instance != "Gehäuse" || mt.Unit != "°C" || ck.Message != "23,5 °C" ||
		ck.Group != "Eigene Werte" || ck.Name != "Gehäuse" {
		t.Errorf("scaled: %+v", ck)
	}
	if ck := mustCheck(t, m, "snmp:custom:Fehlt"); ck.State != model.StateUnknown {
		t.Errorf("missing OID: %+v", ck)
	}
	if ck := mustCheck(t, m, "snmp:custom:Text"); ck.State != model.StateOK || ck.Message != "Alles gut" || len(ck.Metrics) != 0 {
		t.Errorf("text OID: %+v", ck)
	}
	if mt, ok := metric(mustCheck(t, m, "snmp:custom:Zahl als Text"), "custom"); !ok || mt.Value != 41.5 {
		t.Errorf("numeric text: %+v", mt)
	}
	if ck := mustCheck(t, m, "snmp:custom:Kaputt"); ck.State != model.StateUnknown || ck.Message != "Ungültige OID" {
		t.Errorf("invalid OID: %+v", ck)
	}
}

func TestUnreachable(t *testing.T) {
	sw := fakesnmp.NewSwitch(4)
	sw.Community = "geheim"
	c, _ := setup(map[string]*fakesnmp.Device{"sw": sw})
	ctx := context.Background()

	if _, err := c.Collect(ctx, device("nowhere", model.SNMPConfig{Community: "geheim"})); err == nil {
		t.Error("unknown host must fail")
	}
	if _, err := c.Collect(ctx, device("sw", model.SNMPConfig{})); err == nil {
		t.Error("wrong community must fail")
	}
	if _, err := c.Test(ctx, "sw", model.SNMPConfig{Community: "falsch"}); err == nil {
		t.Error("Test with wrong community must fail")
	}
	sw.SetReachable(false)
	if _, err := c.Collect(ctx, device("sw", model.SNMPConfig{Community: "geheim"})); !errors.Is(err, fakesnmp.ErrTimeout) {
		t.Errorf("unreachable: %v", err)
	}
	sw.SetReachable(true)
	if _, err := c.Collect(ctx, device("sw", model.SNMPConfig{Community: "geheim"})); err != nil {
		t.Errorf("reachable again: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.Collect(cancelled, device("sw", model.SNMPConfig{Community: "geheim"})); err == nil {
		t.Error("cancelled context must fail")
	}
}

// flaky fails walks below one subtree.
type flaky struct {
	snmp.Session
	fail string
}

func (f flaky) Walk(root string) ([]gosnmp.SnmpPDU, error) {
	if strings.HasPrefix(strings.TrimPrefix(root, "."), f.fail) {
		return nil, fakesnmp.ErrTimeout
	}
	return f.Session.Walk(root)
}

func TestPartialFailure(t *testing.T) {
	sw := fakesnmp.NewSwitch(8)
	c, _ := setup(map[string]*fakesnmp.Device{"sw": sw})
	dial := c.Dial
	c.Dial = func(ctx context.Context, host string, cfg model.SNMPConfig, to time.Duration) (snmp.Session, error) {
		s, err := dial(ctx, host, cfg, to)
		if err != nil {
			return nil, err
		}
		return flaky{Session: s, fail: "1.3.6.1.2.1.2.2"}, nil
	}
	m := collect(t, c, device("sw", model.SNMPConfig{Profile: "switch", Interfaces: []string{"Gi1/0/1"}}))
	if ck := mustCheck(t, m, "snmp:system"); ck.State != model.StateUnknown || !strings.Contains(ck.Message, "Schnittstellen") {
		t.Errorf("system: %+v", ck)
	}
	if ck := mustCheck(t, m, "snmp:if:Gi1/0/1"); ck.State != model.StateUnknown {
		t.Errorf("watched port: %+v", ck)
	}
	mustCheck(t, m, "snmp:cpu")
}
