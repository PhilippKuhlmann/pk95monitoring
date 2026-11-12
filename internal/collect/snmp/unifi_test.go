package snmp_test

import (
	"context"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"pk95monitoring/internal/collect/snmp"
	"pk95monitoring/internal/fake/fakesnmp"
	"pk95monitoring/internal/model"
)

func TestUniFiSwitch(t *testing.T) {
	sw := fakesnmp.NewUniFiSwitch()
	c, clk := setup(map[string]*fakesnmp.Device{"usw": sw})
	d := device("usw", model.SNMPConfig{})
	collect(t, c, d)
	clk.advance(time.Minute)
	m := collect(t, c, d)

	sys := mustCheck(t, m, "snmp:system")
	if sys.State != model.StateOK || sys.Facts["Profil"] != "UniFi-Switch" || sys.Facts["Modell"] != "USW-Pro-24-PoE" ||
		sys.Facts["Firmware"] != "7.0.50.15613" || len(sys.Facts["Seriennummer"]) != 12 {
		t.Errorf("system: %+v", sys)
	}
	if cpu := mustCheck(t, m, "snmp:cpu"); len(cpu.Metrics) != 1 || cpu.Metrics[0].Value <= 0 || cpu.Metrics[0].Value > 20 ||
		!strings.Contains(cpu.Message, "60 s") {
		t.Errorf("cpu: %+v", cpu)
	}
	mem := mustCheck(t, m, "snmp:mem")
	if u, ok := metric(mem, "mem.usage"); !ok || u.Value < 50 || u.Value > 65 {
		t.Errorf("mem: %+v", mem)
	}
	if tot, _ := metric(mem, "mem.total"); tot.Value != 1015808*1024 {
		t.Errorf("mem.total = %v", tot.Value)
	}
	for _, k := range []string{"snmp:temp:Sensor 1", "snmp:temp:Sensor 2"} {
		ck := mustCheck(t, m, k)
		if mt, ok := metric(ck, "temp"); !ok || mt.Unit != "°C" || mt.Instance != strings.TrimPrefix(k, "snmp:temp:") ||
			mt.Value < 40 || mt.Value > 62 || ck.State != model.StateOK || ck.Group != "Sensoren" {
			t.Errorf("%s: %+v", k, ck)
		}
	}
	for _, k := range []string{"snmp:fan:Lüfter 1", "snmp:fan:Lüfter 2"} {
		ck := mustCheck(t, m, k)
		if mt, ok := metric(ck, "fan.rpm"); !ok || mt.Unit != "rpm" || mt.Value < 3000 || mt.Value > 4200 ||
			ck.State != model.StateOK || !strings.HasSuffix(ck.Message, "U/min") {
			t.Errorf("%s: %+v", k, ck)
		}
	}
	if ck := mustCheck(t, m, "snmp:psu:Netzteil 1"); ck.State != model.StateOK || ck.Message != "In Betrieb" {
		t.Errorf("psu: %+v", ck)
	}

	// 24 access ports and 2 SFP+; the CPU interface and the idle link aggregates are hidden
	ports := 0
	for k := range m {
		if strings.HasPrefix(k, "snmp:if:") {
			ports++
		}
	}
	if ports != 26 {
		t.Errorf("%d interface checks, want 26", ports)
	}

	// PoE: budget, consumption and usage; each powered port with facts and poe.port
	poe := mustCheck(t, m, "snmp:poe")
	budget, _ := metric(poe, "poe.budget")
	power, _ := metric(poe, "poe.power")
	usage, _ := metric(poe, "poe.usage")
	if poe.Name != "PoE" || poe.Group != "PoE" || poe.State != model.StateOK || budget.Value != 400 || budget.Unit != "W" ||
		power.Value < 90 || power.Value > 130 || power.Unit != "W" || math.Abs(usage.Value-power.Value/4) > 1e-9 ||
		usage.Unit != "%" || poe.Facts["Versorgte Geräte"] != "12" || !strings.Contains(poe.Message, "12 Ports versorgt") {
		t.Errorf("snmp:poe: %+v", poe)
	}
	var sum float64
	powered := 0
	for k, ck := range m {
		if mt, ok := metric(ck, "poe.port"); ok {
			powered++
			sum += mt.Value
			want := "aktiv · " + germanW(mt.Value) + " W"
			if mt.Unit != "W" || mt.Instance != strings.TrimPrefix(k, "snmp:if:") || ck.Facts["PoE"] != want {
				t.Errorf("%s: metric %+v, fact %q, want %q", k, mt, ck.Facts["PoE"], want)
			}
		}
	}
	if powered != 12 || math.Abs(sum-power.Value) > 1 {
		t.Errorf("%d powered ports with %v W, snmp:poe %v W", powered, sum, power.Value)
	}
	if ck := mustCheck(t, m, "snmp:if:0/3"); ck.Facts["PoE-Klasse"] != "4" || !strings.HasPrefix(ck.Facts["PoE"], "aktiv · 2") {
		t.Errorf("AP on port 3: %+v", ck.Facts)
	}
	if ck := mustCheck(t, m, "snmp:if:0/7"); ck.Facts["PoE-Klasse"] != "2" {
		t.Errorf("phone on port 7: %+v", ck.Facts)
	}
	if ck := mustCheck(t, m, "snmp:if:0/21"); ck.Facts["PoE"] != "aus" || ck.Facts["PoE-Klasse"] != "" || len(ck.Metrics) != 0 {
		t.Errorf("port without link: %+v", ck)
	}
	if ck := mustCheck(t, m, "snmp:if:0/25"); ck.Facts["PoE"] != "" {
		t.Errorf("SFP+ port has no PoE: %+v", ck.Facts)
	}
	if rx, ok := metric(mustCheck(t, m, "snmp:if:0/1"), "if.rx"); !ok || rx.Instance != "0/1" {
		t.Errorf("port chart series: %+v", rx)
	}

	// LLDP: lldpRemLocalPortNum = bridge port = ifIndex ("Port <n>" matches no ifName)
	for port, want := range map[string][2]string{
		"0/1":  {"AP-Buero-EG", "eth0"},
		"0/24": {"UDM-Pro", "Port 9"},
		"0/25": {"sw-core", "Te1/1/1"},
		"0/13": {"pve1", "eno1"}, // port id is a MAC: the description is shown
	} {
		ck := mustCheck(t, m, "snmp:if:"+port)
		if ck.Facts["Nachbar"] != want[0] || ck.Facts["Nachbar-Port"] != want[1] {
			t.Errorf("%s neighbor: %+v, want %v", port, ck.Facts, want)
		}
	}
	cam := mustCheck(t, m, "snmp:if:0/4") // no system name: chassis MAC
	if !isMAC(cam.Facts["Nachbar"]) || !isMAC(cam.Facts["Nachbar-Port"]) {
		t.Errorf("camera neighbor: %+v", cam.Facts)
	}
	if ck := mustCheck(t, m, "snmp:if:0/12"); ck.Facts["Nachbar"] != "" {
		t.Errorf("port 12 has no neighbor: %+v", ck.Facts)
	}

	// failures: PoE overload, PoE switched off, a failed fan
	sw.SetPoEFault(5, true)
	sw.SetPoE(7, false)
	sw.SetFanFailed(2, true)
	clk.advance(time.Minute)
	m = collect(t, c, d)
	if ck := mustCheck(t, m, "snmp:if:0/5"); ck.Facts["PoE"] != "Fehler" || ck.Facts["PoE-Fehler"] != "Überlast" || ck.State != model.StateOK {
		t.Errorf("faulty PoE port: %+v", ck)
	}
	if ck := mustCheck(t, m, "snmp:if:0/7"); ck.Facts["PoE"] != "aus" {
		t.Errorf("PoE off: %+v", ck.Facts)
	}
	if ck := mustCheck(t, m, "snmp:poe"); ck.State != model.StateWarn || !strings.Contains(ck.Message, "PoE-Fehler an 0/5") ||
		ck.Facts["Versorgte Geräte"] != "10" {
		t.Errorf("snmp:poe with a fault: %+v", ck)
	}
	if ck := mustCheck(t, m, "snmp:fan:Lüfter 2"); ck.State != model.StateCrit || ck.Message != "Lüfter ausgefallen" {
		t.Errorf("failed fan: %+v", ck)
	}
}

// germanW formats watts like the collector: one decimal, decimal comma, no trailing zero.
func germanW(v float64) string {
	f := strings.TrimRight(strings.TrimRight(strconv.FormatFloat(v, 'f', 1, 64), "0"), ".")
	return strings.Replace(f, ".", ",", 1)
}

func isMAC(s string) bool {
	if len(s) != 17 {
		return false
	}
	for i, r := range s {
		if i%3 == 2 && r != ':' {
			return false
		}
	}
	return true
}

// Real values recorded from UniFi switches (LibreNMS snmpsim): the CPU string of a US-8-v2,
// its memory (113772 of 256232 KB free) and the four sensors of an EdgeSwitch.
func TestUniFiRealSampleValues(t *testing.T) {
	sw := fakesnmp.NewUniFiSwitch()
	const fp, box = "1.3.6.1.4.1.4413.1.1.1.1.4.", "1.3.6.1.4.1.4413.1.1.43.1."
	sw.Set(fp+"9.0", gosnmp.OctetString, "    5 Secs ( 40.8140%)   60 Secs ( 18.1657%)  300 Secs ( 15.1503%)")
	sw.Set(fp+"1.0", gosnmp.Integer, 113772)
	sw.Set(fp+"2.0", gosnmp.Integer, 256232)
	sw.Hide(box + "8.1")
	for i, v := range []int{70, 59, 66, 66} {
		sw.Set(box+"8.1.5.1."+string(rune('0'+i)), gosnmp.Integer, v)
	}
	sw.Set(box+"15.1.2.1", gosnmp.Integer, 2) // unit state warning
	sw.Set(box+"6.1.4.1.0", gosnmp.Integer, 5200)
	c, _ := setup(map[string]*fakesnmp.Device{"usw": sw})
	m := collect(t, c, device("usw", model.SNMPConfig{}))

	if cpu := mustCheck(t, m, "snmp:cpu"); cpu.Metrics[0].Value != 18.1657 || cpu.Message != "Auslastung 18 % (Mittel über 60 s)" {
		t.Errorf("cpu: %+v", cpu)
	}
	mem := mustCheck(t, m, "snmp:mem")
	if u, _ := metric(mem, "mem.usage"); math.Abs(u.Value-55.598) > 0.01 {
		t.Errorf("mem.usage = %v, want 55.6", u.Value)
	}
	if used, _ := metric(mem, "mem.used"); used.Value != (256232-113772)*1024 {
		t.Errorf("mem.used = %v", used.Value)
	}
	for name, want := range map[string]float64{"Sensor 1": 70, "Sensor 2": 59, "Sensor 3": 66, "Sensor 4": 66} {
		ck := mustCheck(t, m, "snmp:temp:"+name)
		if mt, _ := metric(ck, "temp"); mt.Value != want {
			t.Errorf("%s = %v, want %v", name, mt.Value, want)
		}
	}
	// the unit warning marks the hottest sensor
	if ck := mustCheck(t, m, "snmp:temp:Sensor 1"); ck.State != model.StateWarn || !strings.Contains(ck.Message, "Temperaturwarnung") {
		t.Errorf("hottest sensor: %+v", ck)
	}
	if ck := mustCheck(t, m, "snmp:temp:Sensor 2"); ck.State != model.StateOK {
		t.Errorf("sensor 2: %+v", ck)
	}
	if mt, _ := metric(mustCheck(t, m, "snmp:fan:Lüfter 1"), "fan.rpm"); mt.Value != 5200 {
		t.Errorf("integer fan speed: %+v", mt)
	}

	// above the normal range (max 85 °C)
	sw.Set(box+"8.1.5.1.1", gosnmp.Integer, 91)
	m = collect(t, c, device("usw", model.SNMPConfig{}))
	if ck := mustCheck(t, m, "snmp:temp:Sensor 2"); ck.State != model.StateWarn || !strings.Contains(ck.Message, "85 °C") {
		t.Errorf("hot sensor: %+v", ck)
	}

	// consumption reported in mW is scaled
	sw.Set("1.3.6.1.2.1.105.1.3.1.1.4.1", gosnmp.Gauge32, uint(98000))
	m = collect(t, c, device("usw", model.SNMPConfig{}))
	if p, _ := metric(mustCheck(t, m, "snmp:poe"), "poe.power"); p.Value != 98 {
		t.Errorf("poe.power from mW = %v", p.Value)
	}
}

func TestUniFiDetection(t *testing.T) {
	const machine = "1.3.6.1.4.1.4413.1.1.1.1.1.2.0"
	tests := []struct {
		descr, objectID, machineType, want string
	}{
		{"US-24-250W, 5.43.18.12487, Linux 3.6.5", "1.3.6.1.4.1.4413", "", snmp.ProfileUniFi},
		{"USW-24P-250, 3.7.17.5220, Linux 3.6.5", "1.3.6.1.4.1.4413", "", snmp.ProfileUniFi},
		{"UBNT US48PRO - 48 GE POE + 4 XE, 4.0.19.9951, Linux 3.6.5", "1.3.6.1.4.1.4413", "", snmp.ProfileUniFi},
		{"USW GigaBit Ethernet Switch, firmware 5.76.7.13442", "1.3.6.1.4.1.41112.1", "", snmp.ProfileUniFi},
		{"something", "1.3.6.1.4.1.41112.1", "", snmp.ProfileUniFi}, // US-8 by sysObjectID alone
		{"USW-Flex-XG, 6.2.14.13855", "1.3.6.1.4.1", "", snmp.ProfileUniFi},
		{"USW-Flex 5.76.7.13442", "1.3.6.1.4.1.8072.3.2.10", "", snmp.ProfileUniFi},
		{"Linux UBNT 4.9.65 #0 Thu Aug 30 12:10:54 2018 mips", "1.3.6.1.4.1.8072.3.2.10", "", snmp.ProfileUniFi},
		{"Switch, 7.0.50.15613", "1.3.6.1.4.1.4413", "USW-Pro-24-PoE", snmp.ProfileUniFi},
		{"EdgeSwitch 24-Port 250W, 1.0.1.4720839, Linux 3.6.5", "1.3.6.1.4.1.4413", "EdgeSwitch 24-Port 250W", snmp.ProfileUniFi},
		{"Netgear GS728TP", "1.3.6.1.4.1.4413", "GS728TP", snmp.ProfileGeneric},
		{"Linux Switch1 3.18.24 #0 mips", "1.3.6.1.4.1.8072.3.2.10", "", snmp.ProfileGeneric},
		{"UAP-AC-Pro-Gen2 6.5.28", "1.3.6.1.4.1.41112", "", snmp.ProfileGeneric},
		{"USP-RPS", "1.3.6.1.4.1.41112", "", snmp.ProfileGeneric},
	}
	for _, tt := range tests {
		t.Run(tt.descr, func(t *testing.T) {
			sw := fakesnmp.NewSwitch(4) // 4 ports: "generic" unless detected as UniFi
			sw.Set("1.3.6.1.2.1.1.1.0", gosnmp.OctetString, tt.descr)
			sw.Set("1.3.6.1.2.1.1.2.0", gosnmp.ObjectIdentifier, "."+tt.objectID)
			if tt.machineType != "" {
				sw.Set(machine, gosnmp.OctetString, tt.machineType)
			}
			c, _ := setup(map[string]*fakesnmp.Device{"sw": sw})
			res, err := c.Test(context.Background(), "sw", model.SNMPConfig{})
			if err != nil || res.Profile != tt.want {
				t.Errorf("profile %q (%v), want %q", res.Profile, err, tt.want)
			}
		})
	}
	if res, err := setupTest(t, fakesnmp.NewUniFiSwitch()); err != nil || res.Profile != snmp.ProfileUniFi {
		t.Errorf("UniFi fake: %+v %v", res, err)
	}
	// the configured profile is accepted as is
	if cfg := snmp.WithDefaults(model.SNMPConfig{Profile: " UniFi "}); cfg.Profile != snmp.ProfileUniFi {
		t.Errorf("profile %q", cfg.Profile)
	}
}

func setupTest(t *testing.T, d *fakesnmp.Device) (snmp.TestResult, error) {
	t.Helper()
	c, _ := setup(map[string]*fakesnmp.Device{"d": d})
	return c.Test(context.Background(), "d", model.SNMPConfig{})
}

// A UniFi switch on net-snmp (USW-24 and others): no FASTPATH tree, CPU from
// HOST-RESOURCES-MIB, memory from UCD-SNMP-MIB.
func TestUniFiNetSNMP(t *testing.T) {
	sw := fakesnmp.NewSwitch(4)
	sw.Set("1.3.6.1.2.1.1.1.0", gosnmp.OctetString, "Linux UBNT 4.4.153 #0 Mon Oct 1 2018 mips")
	sw.Set("1.3.6.1.2.1.1.2.0", gosnmp.ObjectIdentifier, ".1.3.6.1.4.1.8072.3.2.10")
	sw.Hide("1.0.8802")
	const ucd = "1.3.6.1.4.1.2021.4."
	sw.Set(ucd+"5.0", gosnmp.Integer, 249088)
	sw.Set(ucd+"6.0", gosnmp.Integer, 100000)
	sw.Set(ucd+"14.0", gosnmp.Integer, 9088)
	sw.Set(ucd+"15.0", gosnmp.Integer, 40000)
	c, _ := setup(map[string]*fakesnmp.Device{"sw": sw})
	m := collect(t, c, device("sw", model.SNMPConfig{}))
	sys := mustCheck(t, m, "snmp:system")
	if sys.State != model.StateOK || sys.Facts["Profil"] != "UniFi-Switch" || sys.Facts["Modell"] != "" {
		t.Errorf("system: %+v", sys)
	}
	mustCheck(t, m, "snmp:cpu")
	mem := mustCheck(t, m, "snmp:mem")
	if u, _ := metric(mem, "mem.used"); u.Value != 100000*1024 {
		t.Errorf("UCD memory: %+v", mem)
	}
	for k := range m {
		if strings.HasPrefix(k, "snmp:temp:") || strings.HasPrefix(k, "snmp:fan:") || k == "snmp:poe" {
			t.Errorf("unexpected %s", k)
		}
	}
}

// Switch profiles on a device without any of the switch MIBs produce no errors.
func TestSwitchProfilesWithoutTables(t *testing.T) {
	for _, profile := range []string{"switch", "unifi"} {
		sw := fakesnmp.NewSwitch(2)
		for _, h := range []string{"1.3.6.1.2.1.2", "1.3.6.1.2.1.31", "1.3.6.1.2.1.25", "1.3.6.1.2.1.17", "1.0.8802"} {
			sw.Hide(h)
		}
		c, _ := setup(map[string]*fakesnmp.Device{"sw": sw})
		m := collect(t, c, device("sw", model.SNMPConfig{Profile: profile}))
		if ck := mustCheck(t, m, "snmp:system"); ck.State != model.StateOK || len(m) != 1 {
			t.Errorf("%s: %d checks, system %+v", profile, len(m), ck)
		}
	}

	// a forced "unifi" profile on an ordinary switch keeps its LLDP neighbors
	sw := fakesnmp.NewSwitch(8)
	c, _ := setup(map[string]*fakesnmp.Device{"sw": sw})
	m := collect(t, c, device("sw", model.SNMPConfig{Profile: "unifi"}))
	if ck := mustCheck(t, m, "snmp:system"); ck.State != model.StateOK {
		t.Errorf("system: %+v", ck)
	}
	if ck := mustCheck(t, m, "snmp:if:Gi1/0/1"); ck.Facts["Nachbar"] != "fw-01" {
		t.Errorf("neighbor: %+v", ck.Facts)
	}
}

func TestGenericSwitchLLDP(t *testing.T) {
	sw := fakesnmp.NewSwitch(24)
	c, _ := setup(map[string]*fakesnmp.Device{"sw": sw})
	m := collect(t, c, device("sw", model.SNMPConfig{}))
	// lldpLocPortNum 501… differs from ifIndex: mapped by lldpLocPortId = ifName
	for port, want := range map[string][2]string{
		"Gi1/0/1": {"fw-01", "port1"}, "Gi1/0/3": {"pve2", "eno1"}, "Te1/1/1": {"sw-kern", "Te1/0/1"},
	} {
		ck := mustCheck(t, m, "snmp:if:"+port)
		if ck.Facts["Nachbar"] != want[0] || ck.Facts["Nachbar-Port"] != want[1] {
			t.Errorf("%s: %+v", port, ck.Facts)
		}
	}
	if _, ok := m["snmp:poe"]; ok {
		t.Error("switch without PoE has snmp:poe")
	}
	// generic profile: no switch MIBs
	m = collect(t, c, device("sw", model.SNMPConfig{Profile: "generic"}))
	if ck := mustCheck(t, m, "snmp:if:Gi1/0/1"); ck.Facts["Nachbar"] != "" {
		t.Errorf("generic profile reads LLDP: %+v", ck.Facts)
	}
}

func TestPoETimeoutIsPartial(t *testing.T) {
	sw := fakesnmp.NewUniFiSwitch()
	c, _ := setup(map[string]*fakesnmp.Device{"usw": sw})
	dial := c.Dial
	c.Dial = func(ctx context.Context, host string, cfg model.SNMPConfig, to time.Duration) (snmp.Session, error) {
		s, err := dial(ctx, host, cfg, to)
		if err != nil {
			return nil, err
		}
		return flaky{Session: s, fail: "1.3.6.1.2.1.105"}, nil
	}
	m := collect(t, c, device("usw", model.SNMPConfig{}))
	if ck := mustCheck(t, m, "snmp:system"); ck.State != model.StateUnknown || !strings.Contains(ck.Message, "PoE") {
		t.Errorf("system: %+v", ck)
	}
	if _, ok := m["snmp:poe"]; ok {
		t.Error("snmp:poe without data")
	}
	mustCheck(t, m, "snmp:fan:Lüfter 1")
	if ck := mustCheck(t, m, "snmp:if:0/1"); ck.Facts["Nachbar"] != "AP-Buero-EG" || ck.Facts["PoE"] != "" {
		t.Errorf("port 1: %+v", ck.Facts)
	}
}
