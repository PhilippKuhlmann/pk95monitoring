package snmp

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"pk95monitoring/internal/model"
)

// mapSession is a Session over a fixed set of objects (keys without leading dot).
type mapSession struct {
	m     map[string]gosnmp.SnmpPDU
	walks []string
}

func newMapSession() *mapSession { return &mapSession{m: map[string]gosnmp.SnmpPDU{}} }

func (s *mapSession) set(oid string, typ gosnmp.Asn1BER, v any) {
	if str, ok := v.(string); ok && typ == gosnmp.OctetString {
		v = []byte(str)
	}
	s.m[oid] = gosnmp.SnmpPDU{Name: "." + oid, Type: typ, Value: v}
}

func (s *mapSession) Get(oids []string) ([]gosnmp.SnmpPDU, error) {
	out := make([]gosnmp.SnmpPDU, 0, len(oids))
	for _, o := range oids {
		if p, ok := s.m[trimDot(o)]; ok {
			out = append(out, p)
		} else {
			out = append(out, gosnmp.SnmpPDU{Name: "." + trimDot(o), Type: gosnmp.NoSuchObject})
		}
	}
	return out, nil
}

func (s *mapSession) Walk(root string) ([]gosnmp.SnmpPDU, error) {
	root = trimDot(root)
	s.walks = append(s.walks, root)
	var keys []string
	for k := range s.m {
		if k == root || strings.HasPrefix(k, root+".") {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, CompareOID)
	out := make([]gosnmp.SnmpPDU, len(keys))
	for i, k := range keys {
		out[i] = s.m[k]
	}
	return out, nil
}

func (s *mapSession) Close() error { return nil }

func collectMap(t *testing.T, c *Collector, s *mapSession, cfg model.SNMPConfig) map[string]model.Check {
	t.Helper()
	c.Dial = func(context.Context, string, model.SNMPConfig, time.Duration) (Session, error) { return s, nil }
	checks, err := c.Collect(context.Background(), model.Device{ID: "d", Host: "h", SNMP: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]model.Check{}
	for _, ck := range checks {
		m[ck.Key] = ck
	}
	return m
}

func TestParseFastpathCPU(t *testing.T) {
	tests := []struct {
		in     string
		want   float64
		window int
		ok     bool
	}{
		{"    5 Secs ( 40.8140%)   60 Secs ( 18.1657%)  300 Secs ( 15.1503%)", 18.1657, 60, true},
		{"    5 Secs ( 96.4918%)   60 Secs ( 54.2271%)  300 Secs ( 38.2591%)", 54.2271, 60, true},
		{"   10 Secs ( 12.5000%)  300 Secs (  9.1000%)", 12.5, 10, true},
		{"60 Sec (7%)", 7, 60, true},
		{"CPU 23.5%", 23.5, 0, true},
		{"", 0, 0, false},
		{"n/a", 0, 0, false},
		{"5 Secs ( 140.0%)", 0, 0, false},
	}
	for _, tt := range tests {
		got, w, ok := parseFastpathCPU(tt.in)
		if ok != tt.ok || (ok && (got != tt.want || w != tt.window)) {
			t.Errorf("parseFastpathCPU(%q) = %v, %d, %v; want %v, %d, %v", tt.in, got, w, ok, tt.want, tt.window, tt.ok)
		}
	}
}

func TestParseUniFiDescr(t *testing.T) {
	tests := []struct{ in, hw, ver string }{
		{"US-24-250W, 5.43.18.12487, Linux 3.6.5", "US-24-250W", "5.43.18.12487"},
		{"USW-24P-250, 3.7.17.5220, Linux 3.6.5", "USW-24P-250", "3.7.17.5220"},
		{"US-8, 5.76.7.13442, Linux 3.6.5", "US-8", "5.76.7.13442"},
		{"UBNT US48PRO - 48 GE POE + 4 XE, 4.0.19.9951, Linux 3.6.5", "US48PRO - 48 GE POE + 4 XE", "4.0.19.9951"},
		{"USW GigaBit Ethernet Switch, firmware 5.76.7.13442", "USW GigaBit Ethernet Switch", "5.76.7.13442"},
		{"USW-Flex-XG, 6.2.14.13855", "USW-Flex-XG", "6.2.14.13855"},
		{"USW-Flex 5.76.7.13442", "USW-Flex", "5.76.7.13442"},
		{"EdgeSwitch 24-Port 250W, 1.0.1.4720839, Linux 3.6.5-f4a26ed5", "EdgeSwitch 24-Port 250W", "1.0.1.4720839"},
		{"Linux UBNT 4.4.153 #0 mips", "", ""},
		{"Cisco IOS Software, C2960X", "", ""},
	}
	for _, tt := range tests {
		if hw, ver := parseUniFiDescr(tt.in); hw != tt.hw || ver != tt.ver {
			t.Errorf("parseUniFiDescr(%q) = %q, %q; want %q, %q", tt.in, hw, ver, tt.hw, tt.ver)
		}
	}
}

func TestLLDPIDAndMAC(t *testing.T) {
	raw := []byte{0x20, 0x4C, 0x9E, 0x6E, 0x08, 0xFE}
	if got := lldpID(gosnmp.SnmpPDU{Type: gosnmp.OctetString, Value: raw}, false); got != "20:4C:9E:6E:08:FE" {
		t.Errorf("raw chassis id: %q", got)
	}
	text := []byte("20-4C-9E-6E-08-FE")
	if got := lldpID(gosnmp.SnmpPDU{Type: gosnmp.OctetString, Value: text}, true); got != "20-4C-9E-6E-08-FE" {
		t.Errorf("text chassis id: %q", got)
	}
	// six printable bytes are a MAC only with the macAddress subtype
	if got := lldpID(gosnmp.SnmpPDU{Type: gosnmp.OctetString, Value: []byte("eth0/1")}, false); got != "eth0/1" {
		t.Errorf("printable id: %q", got)
	}
	if got := lldpID(gosnmp.SnmpPDU{Type: gosnmp.OctetString, Value: []byte("eth0/1")}, true); got != "65:74:68:30:2F:31" {
		t.Errorf("printable id with MAC subtype: %q", got)
	}
	if lldpID(gosnmp.SnmpPDU{Type: gosnmp.NoSuchInstance}, true) != "" {
		t.Error("missing id must be empty")
	}
	if !isMACText("20-4C-9E-6E-09-00") || !isMACText("20:4c:9e:6e:09:00") || isMACText("gi1") || isMACText("20-4C-9E-6E-09-0G") {
		t.Error("isMACText")
	}
}

func TestPoEDecorate(t *testing.T) {
	f := iface{index: "5", name: "0/5"}
	tests := []struct {
		pp    poePort
		poe   string
		class string
		watts float64 // 0 = no metric
		fault string
	}{
		{poePort{admin: 1, detect: 3, class: 4, mw: 6200, hasPower: true, hasFault: true}, "aktiv · 6,2 W", "3", 6.2, ""},
		{poePort{admin: 1, detect: 3, class: 5}, "aktiv", "4", 0, ""},
		{poePort{admin: 1, detect: 2, class: 1, hasPower: true, hasFault: true, fault: 1}, "aus", "", 0, ""},
		{poePort{admin: 2, detect: 1}, "aus", "", 0, ""},
		{poePort{admin: 1, detect: 4}, "Fehler", "", 0, ""},
		{poePort{admin: 1, detect: 6}, "Fehler", "", 0, ""},
		{poePort{admin: 1, detect: 3, mw: 12000, hasPower: true, fault: 3, hasFault: true}, "Fehler", "", 0, "Überlast"},
		{poePort{admin: 1, detect: 5}, "Test", "", 0, ""},
		{poePort{mw: 4500, hasPower: true}, "aktiv · 4,5 W", "", 4.5, ""}, // vendor table only
	}
	for i, tt := range tests {
		ck := model.Check{Facts: map[string]string{}}
		pp := tt.pp
		(&portExtra{poe: &pp}).decorate(&ck, f)
		if ck.Facts["PoE"] != tt.poe || ck.Facts["PoE-Klasse"] != tt.class || ck.Facts["PoE-Fehler"] != tt.fault {
			t.Errorf("%d: facts %v", i, ck.Facts)
		}
		var w float64
		for _, m := range ck.Metrics {
			if m.Name == "poe.port" && m.Instance == "0/5" && m.Unit == "W" {
				w = m.Value
			}
		}
		if w != tt.watts {
			t.Errorf("%d: poe.port %v, want %v", i, w, tt.watts)
		}
	}
}

func TestPoEIfIndexMapping(t *testing.T) {
	ifs := []iface{
		{index: "1", name: "lo", typ: 24},
		{index: "2", name: "eth0", typ: 6},
		{index: "1000001", name: "GigabitEthernet 1/1"},
		{index: "1000002", name: "GigabitEthernet 1/2"},
	}
	ix := newIfIndexer(ifs)
	base := map[string]string{"1": "1000001", "2": "1000002"}
	if got := poeIfIndex("1.1", 0, true, base, ix); got != "1000001" {
		t.Errorf("by base port: %q", got)
	}
	if got := poeIfIndex("1.2", 1, true, map[string]string{}, ix); got != "2" {
		t.Errorf("by ifIndex: %q", got) // port 2 is the Ethernet ifIndex 2
	}
	if got := poeIfIndex("2.1", 1, false, base, ix); got != "1000001" {
		t.Errorf("several groups, by position: %q", got)
	}
	if got := poeIfIndex("1.9", 7, true, base, ix); got != "" {
		t.Errorf("beyond the ports: %q", got)
	}
}

// vitesse builds a UniFi US-8 (Vitesse): switch ports only in ifXTable (1000001…),
// LLDP local port = bridge port, text chassis ids (values from a real recording).
func vitesse(rx uint64) *mapSession {
	s := newMapSession()
	s.set("1.3.6.1.2.1.1.1.0", gosnmp.OctetString, "USW GigaBit Ethernet Switch, firmware 5.76.7.13442")
	s.set("1.3.6.1.2.1.1.2.0", gosnmp.ObjectIdentifier, ".1.3.6.1.4.1.41112.1")
	s.set("1.3.6.1.2.1.1.3.0", gosnmp.TimeTicks, uint32(100000))
	s.set("1.3.6.1.2.1.1.5.0", gosnmp.OctetString, "us-8")
	s.set("1.3.6.1.2.1.2.2.1.2.1", gosnmp.OctetString, "lo")
	s.set("1.3.6.1.2.1.2.2.1.3.1", gosnmp.Integer, 24)
	s.set("1.3.6.1.2.1.2.2.1.2.2", gosnmp.OctetString, "eth0")
	s.set("1.3.6.1.2.1.2.2.1.3.2", gosnmp.Integer, 6)
	s.set("1.3.6.1.2.1.2.2.1.8.2", gosnmp.Integer, 1)
	for i, n := range []string{"1", "2", "3"} {
		idx := "100000" + n
		s.set("1.3.6.1.2.1.31.1.1.1.1."+idx, gosnmp.OctetString, "GigabitEthernet 1/"+n)
		s.set("1.3.6.1.2.1.31.1.1.1.18."+idx, gosnmp.OctetString, "Gi 1/"+n)
		s.set("1.3.6.1.2.1.31.1.1.1.6."+idx, gosnmp.Counter64, rx*uint64(i+1))
		s.set("1.3.6.1.2.1.31.1.1.1.10."+idx, gosnmp.Counter64, rx)
		s.set("1.3.6.1.2.1.31.1.1.1.15."+idx, gosnmp.Gauge32, uint(1000))
		s.set("1.3.6.1.2.1.17.1.4.1.2."+n, gosnmp.Integer, 1000000+i+1)
		s.set("1.0.8802.1.1.2.1.3.7.1.3."+n, gosnmp.OctetString, "0"+n)
	}
	const rem = "1.0.8802.1.1.2.1.4.1.1."
	s.set(rem+"4.61.1.1", gosnmp.Integer, 4)
	s.set(rem+"5.61.1.1", gosnmp.OctetString, "20-4C-9E-6E-08-FE")
	s.set(rem+"6.61.1.1", gosnmp.Integer, 3)
	s.set(rem+"7.61.1.1", gosnmp.OctetString, "20-4C-9E-6E-09-00")
	s.set(rem+"8.61.1.1", gosnmp.OctetString, "")
	s.set(rem+"9.61.1.1", gosnmp.OctetString, "US8Location")
	// second neighbor on local port 2 without system name, raw MAC chassis id
	s.set(rem+"4.61.2.7", gosnmp.Integer, 4)
	s.set(rem+"5.61.2.7", gosnmp.OctetString, []byte{0x20, 0x4C, 0x9E, 0x6E, 0x08, 0xFE})
	s.set(rem+"6.61.2.7", gosnmp.Integer, 3)
	s.set(rem+"7.61.2.7", gosnmp.OctetString, []byte{0x20, 0x4C, 0x9E, 0x6E, 0x09, 0x01})
	s.set(rem+"8.61.2.7", gosnmp.OctetString, "Port 7")
	s.set("1.3.6.1.2.1.25.3.3.1.2.196608", gosnmp.Integer, 7)
	s.set("1.3.6.1.2.1.47.1.1.1.1.2.1", gosnmp.OctetString, "USW GigaBit Ethernet Switch")
	s.set("1.3.6.1.2.1.47.1.1.1.1.5.1", gosnmp.Integer, 3)
	s.set("1.3.6.1.2.1.47.1.1.1.1.11.1", gosnmp.OctetString, "System MAC: fc-ec-da-bf-69-55")
	s.set("1.3.6.1.2.1.47.1.1.1.1.13.1", gosnmp.OctetString, "UBNT-USW")
	return s
}

func TestUniFiVitesseUS8(t *testing.T) {
	now := time.Unix(1_750_000_000, 0)
	c := &Collector{Now: func() time.Time { return now }}
	m := collectMap(t, c, vitesse(1_000_000), model.SNMPConfig{})
	sys := m["snmp:system"]
	if sys.State != model.StateOK || sys.Facts["Profil"] != "UniFi-Switch" || sys.Facts["Modell"] != "USW GigaBit Ethernet Switch" ||
		sys.Facts["Firmware"] != "5.76.7.13442" || sys.Facts["Seriennummer"] != "fc-ec-da-bf-69-55" {
		t.Errorf("system: %+v", sys)
	}
	if cpu := m["snmp:cpu"]; len(cpu.Metrics) != 1 || cpu.Metrics[0].Value != 7 {
		t.Errorf("cpu from hrProcessorLoad: %+v", cpu)
	}
	p1 := m["snmp:if:GigabitEthernet 1/1"]
	if p1.Facts["Nachbar"] != "US8Location" || p1.Facts["Nachbar-Port"] != "20-4C-9E-6E-09-00" {
		t.Errorf("port 1 (base port 1 → ifIndex 1000001): %+v", p1.Facts)
	}
	p2 := m["snmp:if:GigabitEthernet 1/2"]
	if p2.Facts["Nachbar"] != "20:4C:9E:6E:08:FE" || p2.Facts["Nachbar-Port"] != "Port 7" {
		t.Errorf("port 2 (MAC chassis, port description): %+v", p2.Facts)
	}
	if _, ok := m["snmp:poe"]; ok {
		t.Error("no POWER-ETHERNET-MIB, no snmp:poe")
	}
	// ports only in ifXTable get traffic charts from their 64-bit counters
	now = now.Add(time.Minute)
	m = collectMap(t, c, vitesse(1_060_000), model.SNMPConfig{})
	var rx float64
	for _, mt := range m["snmp:if:GigabitEthernet 1/1"].Metrics {
		if mt.Name == "if.rx" {
			rx = mt.Value
		}
	}
	if rx != 1000 {
		t.Errorf("if.rx of an ifXTable-only port = %v, want 1000", rx)
	}
}

func TestLLDPFastpathAndFlexXG(t *testing.T) {
	const rem = "1.0.8802.1.1.2.1.4.1.1."
	s := newMapSession()
	s.set("1.3.6.1.2.1.1.1.0", gosnmp.OctetString, "USW-Flex-XG, 6.2.14.13855")
	s.set("1.3.6.1.2.1.1.2.0", gosnmp.ObjectIdentifier, ".1.3.6.1.4.1")
	s.set("1.3.6.1.2.1.1.3.0", gosnmp.TimeTicks, uint32(100000))
	for i, n := range []string{"gi1", "te1", "te2", "te3", "te4"} {
		idx := string(rune('1' + i))
		s.set("1.3.6.1.2.1.2.2.1.2."+idx, gosnmp.OctetString, n)
		s.set("1.3.6.1.2.1.2.2.1.3."+idx, gosnmp.Integer, 6)
		s.set("1.3.6.1.2.1.2.2.1.8."+idx, gosnmp.Integer, 1)
		s.set("1.3.6.1.2.1.31.1.1.1.1."+idx, gosnmp.OctetString, n)
		s.set("1.0.8802.1.1.2.1.3.7.1.3."+idx, gosnmp.OctetString, n)
	}
	// Port-Channel and VLAN interfaces (hidden while idle)
	s.set("1.3.6.1.2.1.2.2.1.2.1000", gosnmp.OctetString, "Port-Channel 1")
	s.set("1.3.6.1.2.1.2.2.1.3.1000", gosnmp.Integer, 161)
	s.set("1.3.6.1.2.1.2.2.1.8.1000", gosnmp.Integer, 6)
	s.set("1.3.6.1.2.1.2.2.1.2.100001", gosnmp.OctetString, "VLAN 1")
	s.set("1.3.6.1.2.1.2.2.1.3.100001", gosnmp.Integer, 53)
	s.set("1.3.6.1.2.1.2.2.1.8.100001", gosnmp.Integer, 2)
	// two neighbors on local port 5 (te4), raw chassis id
	for _, r := range []string{"0.5.115", "0.5.127"} {
		s.set(rem+"4."+r, gosnmp.Integer, 4)
		s.set(rem+"5."+r, gosnmp.OctetString, []byte{0x10, 0xDA, 0x43, 0xDA, 0x3C, 0x41})
		s.set(rem+"6."+r, gosnmp.Integer, 5)
	}
	s.set(rem+"7.0.5.115", gosnmp.OctetString, "xmg9")
	s.set(rem+"8.0.5.115", gosnmp.OctetString, "TengigabitEthernet9")
	s.set(rem+"9.0.5.115", gosnmp.OctetString, "sw-agg")
	s.set(rem+"7.0.5.127", gosnmp.OctetString, "xmg10")
	s.set(rem+"9.0.5.127", gosnmp.OctetString, "sw-agg-2")
	// an LLDP row with a malformed index is ignored
	s.set(rem+"5.9.9", gosnmp.OctetString, "bad")

	c := &Collector{Now: func() time.Time { return time.Unix(1_750_000_000, 0) }}
	m := collectMap(t, c, s, model.SNMPConfig{})
	if p := m["snmp:system"].Facts["Profil"]; p != "UniFi-Switch" {
		t.Errorf("profile %q", p)
	}
	te4 := m["snmp:if:te4"]
	if te4.Facts["Nachbar"] != "sw-agg, sw-agg-2" || te4.Facts["Nachbar-Port"] != "xmg9" {
		t.Errorf("te4: %+v", te4.Facts)
	}
	if _, ok := m["snmp:if:Port-Channel 1"]; ok {
		t.Error("idle port channel must be hidden")
	}
	if _, ok := m["snmp:if:VLAN 1"]; ok {
		t.Error("idle VLAN interface must be hidden")
	}
	if m["snmp:system"].State != model.StateOK {
		t.Errorf("system: %+v", m["snmp:system"])
	}
}

func TestLLDPCache(t *testing.T) {
	now := time.Unix(1_750_000_000, 0)
	c := &Collector{Now: func() time.Time { return now }}
	s := vitesse(1000)
	count := func() int {
		n := 0
		for _, w := range s.walks {
			if strings.HasPrefix(w, "1.0.8802") {
				n++
			}
		}
		s.walks = nil
		return n
	}
	collectMap(t, c, s, model.SNMPConfig{})
	if n := count(); n == 0 {
		t.Fatal("LLDP not walked")
	}
	now = now.Add(time.Minute)
	m := collectMap(t, c, s, model.SNMPConfig{})
	if n := count(); n != 0 {
		t.Errorf("LLDP walked again within the cache time: %d walks", n)
	}
	if m["snmp:if:GigabitEthernet 1/1"].Facts["Nachbar"] != "US8Location" {
		t.Error("cached neighbor missing")
	}
	// the neighbor leaves; after the cache time the fact is gone
	for k := range s.m {
		if strings.HasPrefix(k, "1.0.8802.1.1.2.1.4") {
			delete(s.m, k)
		}
	}
	now = now.Add(staticTTL)
	m = collectMap(t, c, s, model.SNMPConfig{})
	if n := count(); n == 0 {
		t.Error("LLDP not refreshed after the cache time")
	}
	if nb := m["snmp:if:GigabitEthernet 1/1"].Facts["Nachbar"]; nb != "" {
		t.Errorf("stale neighbor %q", nb)
	}
}

// The FASTPATH tree below 41112.1.1 (UniFi firmware 4.0.14) is used when 4413.1.1 is empty.
func TestFastpathBaseFallback(t *testing.T) {
	s := newMapSession()
	s.set("1.3.6.1.2.1.1.1.0", gosnmp.OctetString, "US-24-250W, 4.0.14.9736, Linux 3.6.5")
	s.set("1.3.6.1.2.1.1.2.0", gosnmp.ObjectIdentifier, ".1.3.6.1.4.1.4413")
	s.set("1.3.6.1.2.1.1.3.0", gosnmp.TimeTicks, uint32(100000))
	const b = "1.3.6.1.4.1.41112.1.1"
	s.set(b+".1.1.1.2.0", gosnmp.OctetString, "US-24-250W")
	s.set(b+".1.1.1.4.0", gosnmp.OctetString, "f09fc2c7acf2")
	s.set(b+".1.1.4.9.0", gosnmp.OctetString, "    5 Secs ( 40.8140%)   60 Secs ( 18.1657%)  300 Secs ( 15.1503%)")
	s.set(b+".43.1.8.1.5.1.1", gosnmp.Integer, 62)
	s.set(b+".43.1.15.1.2.1", gosnmp.Integer, 1)
	s.set(b+".43.1.15.1.3.1", gosnmp.Integer, 62)

	c := &Collector{Now: func() time.Time { return time.Unix(1_750_000_000, 0) }}
	m := collectMap(t, c, s, model.SNMPConfig{Profile: "unifi"})
	sys := m["snmp:system"]
	if sys.Facts["Modell"] != "US-24-250W" || sys.Facts["Seriennummer"] != "f09fc2c7acf2" || sys.Facts["Firmware"] != "4.0.14.9736" {
		t.Errorf("system facts: %+v", sys.Facts)
	}
	if cpu := m["snmp:cpu"]; len(cpu.Metrics) != 1 || cpu.Metrics[0].Value != 18.1657 {
		t.Errorf("cpu: %+v", cpu)
	}
	if tc := m["snmp:temp:Sensor 1"]; len(tc.Metrics) != 1 || tc.Metrics[0].Value != 62 || tc.Message != "62 °C" {
		t.Errorf("temperature: %+v", tc)
	}
	if _, ok := m["snmp:temp:System"]; ok {
		t.Error("unit temperature duplicates the sensor")
	}
}
