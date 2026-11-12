package fakesnmp

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"pk95monitoring/internal/collect/snmp"
	"pk95monitoring/internal/model"
)

func TestWalkOrder(t *testing.T) {
	d := NewSwitch(12)
	pdus, err := d.Walk("1.3.6.1.2.1.2.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(pdus) == 0 {
		t.Fatal("empty walk")
	}
	for i := 1; i < len(pdus); i++ {
		if snmp.CompareOID(pdus[i-1].Name, pdus[i].Name) >= 0 {
			t.Fatalf("not in OID order: %s before %s", pdus[i-1].Name, pdus[i].Name)
		}
	}
	// numeric, not string order: ifDescr.2 comes before ifDescr.10
	col, _ := d.Walk(".1.3.6.1.2.1.2.2.1.2")
	if col[1].Name != ".1.3.6.1.2.1.2.2.1.2.2" || col[9].Name != ".1.3.6.1.2.1.2.2.1.2.10" {
		t.Errorf("column order: %s, %s", col[1].Name, col[9].Name)
	}
	if none, _ := d.Walk("1.3.6.1.2.1.33"); len(none) != 0 {
		t.Errorf("switch must not have UPS-MIB: %d objects", len(none))
	}
}

func TestGetMissing(t *testing.T) {
	d := NewUPS()
	pdus, err := d.Get([]string{".1.3.6.1.2.1.1.5.0", "1.3.6.1.2.1.99.0"})
	if err != nil {
		t.Fatal(err)
	}
	if string(pdus[0].Value.([]byte)) != "usv-01" || pdus[1].Type != gosnmp.NoSuchObject {
		t.Errorf("got %+v", pdus)
	}
}

func TestValuesMoveWithTime(t *testing.T) {
	now := time.Unix(1_750_000_000, 0)
	d := NewSwitch(4)
	d.SetClock(func() time.Time { return now })
	read := func(oid string) uint64 {
		pdus, err := d.Get([]string{oid})
		if err != nil {
			t.Fatal(err)
		}
		switch v := pdus[0].Value.(type) {
		case uint64:
			return v
		case uint:
			return uint64(v)
		case uint32:
			return uint64(v)
		}
		t.Fatalf("%s: unexpected %T", oid, pdus[0].Value)
		return 0
	}
	const hcIn, uptime = "1.3.6.1.2.1.31.1.1.1.6.1", "1.3.6.1.2.1.1.3.0"
	c1, u1 := read(hcIn), read(uptime)
	now = now.Add(time.Minute)
	c2, u2 := read(hcIn), read(uptime)
	if c2 <= c1 || u2-u1 != 6000 {
		t.Errorf("counter %d → %d, uptime %d → %d", c1, c2, u1, u2)
	}

	// a port that is down does not count
	d.SetPortUp(1, false)
	c3 := read(hcIn)
	now = now.Add(time.Minute)
	if c4 := read(hcIn); c4 != c3 {
		t.Errorf("down port counted: %d → %d", c3, c4)
	}
	d.SetPortUp(1, true)
	now = now.Add(time.Minute)
	if c5 := read(hcIn); c5 <= c3 {
		t.Errorf("port back up does not count: %d → %d", c3, c5)
	}
}

func TestSwitchPorts(t *testing.T) {
	d := NewSwitch(24)
	col := func(c string) map[string]int {
		pdus, err := d.Walk("1.3.6.1.2.1.2.2.1." + c)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]int{}
		for _, p := range pdus {
			m[p.Name[strings.LastIndex(p.Name, ".")+1:]] = p.Value.(int)
		}
		return m
	}
	admin, oper := col("7"), col("8")
	if len(oper) != 28 { // 24 access ports, 2 uplinks, vlan1, loopback
		t.Fatalf("got %d interfaces, want 28", len(oper))
	}
	for i := 1; i <= 26; i++ {
		idx := strconv.Itoa(i)
		wantOper, wantAdmin := 1, 1
		if i >= 19 && i <= 24 {
			wantOper = 2
		}
		if i == 24 {
			wantAdmin = 2
		}
		if oper[idx] != wantOper || admin[idx] != wantAdmin {
			t.Errorf("port %d: oper %d admin %d, want %d %d", i, oper[idx], admin[idx], wantOper, wantAdmin)
		}
	}
	names, _ := d.Get([]string{"1.3.6.1.2.1.31.1.1.1.1.25", "1.3.6.1.2.1.31.1.1.1.15.26"})
	if string(names[0].Value.([]byte)) != "Te1/1/1" || names[1].Value.(uint) != 10000 {
		t.Errorf("uplinks: %+v", names)
	}

	// a disabled port stays down until it is enabled and its link comes back
	d.SetPortUp(24, true)
	if col("8")["24"] != 2 {
		t.Error("disabled port came up")
	}
	d.SetPortEnabled(24, true)
	if col("7")["24"] != 1 || col("8")["24"] != 2 {
		t.Error("enabling must not bring the link up")
	}
	d.SetPortUp(24, true)
	if col("8")["24"] != 1 {
		t.Error("enabled port did not come up")
	}
	d.SetPortEnabled(2, false)
	if col("7")["2"] != 2 || col("8")["2"] != 2 {
		t.Error("disabling must take the link down")
	}
	if small := NewSwitch(4); len(small.ports) != 6 {
		t.Errorf("small switch: %d interfaces, want 6 (no uplinks)", len(small.ports))
	}
}

func TestDialer(t *testing.T) {
	ups := NewUPS()
	ups.Community = "intern"
	dial := Dialer(map[string]*Device{"ups": ups})
	ctx := context.Background()

	if _, err := dial(ctx, "elsewhere", model.SNMPConfig{}, time.Second); !errors.Is(err, ErrTimeout) {
		t.Errorf("unknown host: %v", err)
	}
	s, err := dial(ctx, "ups", model.SNMPConfig{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get([]string{"1.3.6.1.2.1.1.5.0"}); !errors.Is(err, ErrTimeout) {
		t.Errorf("wrong community: %v", err)
	}
	s, _ = dial(ctx, "ups", model.SNMPConfig{Community: "intern"}, time.Second)
	if _, err := s.Get([]string{"1.3.6.1.2.1.1.5.0"}); err != nil {
		t.Errorf("right community: %v", err)
	}
	s, _ = dial(ctx, "ups", model.SNMPConfig{Version: "3", User: "mon"}, time.Second)
	if _, err := s.Walk("1.3.6.1.2.1.33"); err != nil {
		t.Errorf("v3 ignores the community: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Error(err)
	}
}

func TestOverridesAndHide(t *testing.T) {
	d := NewNAS()
	d.Hide("1.3.6.1.4.1.6574")
	d.Set(".1.3.6.1.4.1.6574.1.2.0", gosnmp.Integer, 77)
	pdus, _ := d.Walk("1.3.6.1.4.1.6574")
	if len(pdus) != 1 || pdus[0].Value != 77 {
		t.Errorf("hidden subtree with override: %+v", pdus)
	}
	d.Unset("1.3.6.1.4.1.6574.1.2.0")
	if pdus, _ := d.Walk("1.3.6.1.4.1.6574"); len(pdus) != 0 {
		t.Errorf("after Unset: %+v", pdus)
	}
}

func TestConcurrentUse(t *testing.T) {
	devs := map[string]*Device{"sw": NewSwitch(24), "prn": NewPrinter()}
	c := &snmp.Collector{Dial: Dialer(devs)}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			host := "sw"
			if i%2 == 1 {
				host = "prn"
				devs[host].SetJam(i%4 == 1)
			} else {
				devs[host].SetPortUp(i, i%4 == 0)
			}
			if _, err := c.Collect(context.Background(), model.Device{ID: host, Host: host, SNMP: &model.SNMPConfig{}}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

func TestSetClockKeepsBatteryCharge(t *testing.T) {
	now := time.Unix(1_750_000_000, 0)
	d := NewUPS()
	d.SetClock(func() time.Time { return now })
	now = now.Add(10 * time.Minute)
	d.SetOnBattery(true)
	base := now.Add(-10 * time.Minute)
	d.SetClock(func() time.Time { return base }) // restart at an earlier time
	pdus, err := d.Get([]string{"1.3.6.1.4.1.318.1.1.1.2.2.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := pdus[0].Value.(uint); c > 100 || c < 99 {
		t.Errorf("battery capacity after SetClock = %d %%", c)
	}
}

func TestUniFiSwitchObjects(t *testing.T) {
	d := NewUniFiSwitch()
	now := time.Unix(1_750_000_000, 0)
	d.SetClock(func() time.Time { return now })
	walk := func(root string) []gosnmp.SnmpPDU {
		pdus, err := d.Walk(root)
		if err != nil {
			t.Fatal(err)
		}
		for i := 1; i < len(pdus); i++ {
			if snmp.CompareOID(pdus[i-1].Name, pdus[i].Name) >= 0 {
				t.Fatalf("%s: not in OID order", root)
			}
		}
		return pdus
	}
	detect := func() map[string]int {
		m := map[string]int{}
		for _, p := range walk("1.3.6.1.2.1.105.1.1.1.6") {
			m[p.Name[strings.LastIndex(p.Name, ".")+1:]] = p.Value.(int)
		}
		return m
	}
	if det := detect(); len(det) != 24 || det["1"] != 3 || det["12"] != 2 || det["21"] != 2 {
		t.Errorf("detection status: %v", det)
	}
	delivering := 0
	for _, v := range detect() {
		if v == 3 {
			delivering++
		}
	}
	if delivering != 12 {
		t.Errorf("%d ports deliver power, want 12", delivering)
	}
	if rows := walk("1.0.8802.1.1.2.1.4.1.1.5"); len(rows) != 7 {
		t.Errorf("%d LLDP neighbors, want 7", len(rows))
	}
	if b := walk("1.3.6.1.2.1.17.1.4.1.2"); len(b) != 28 {
		t.Errorf("%d bridge ports, want 28", len(b))
	}

	d.SetPoEFault(1, true)
	d.SetPoE(2, false)
	d.SetPortUp(3, false)
	if det := detect(); det["1"] != 4 || det["2"] != 1 || det["3"] != 2 {
		t.Errorf("after changes: %v", det)
	}
	if rows := walk("1.0.8802.1.1.2.1.4.1.1.5"); len(rows) != 6 {
		t.Errorf("neighbor on a port without link must vanish: %d rows", len(rows))
	}
	d.SetFanFailed(1, true)
	pdus, _ := d.Get([]string{"1.3.6.1.4.1.4413.1.1.43.1.6.1.3.1.0", "1.3.6.1.4.1.4413.1.1.43.1.6.1.4.1.0"})
	if pdus[0].Value != 3 || string(pdus[1].Value.([]byte)) != "0" {
		t.Errorf("failed fan: %+v", pdus)
	}
	cpu, _ := d.Get([]string{"1.3.6.1.4.1.4413.1.1.1.1.4.9.0"})
	if s := string(cpu[0].Value.([]byte)); !strings.Contains(s, "60 Secs (") {
		t.Errorf("cpu string %q", s)
	}
}

func TestSwitchLLDP(t *testing.T) {
	d := NewSwitch(24)
	pdus, err := d.Get([]string{"1.0.8802.1.1.2.1.3.7.1.3.501", "1.0.8802.1.1.2.1.4.1.1.9.0.501.1"})
	if err != nil {
		t.Fatal(err)
	}
	if string(pdus[0].Value.([]byte)) != "Gi1/0/1" || string(pdus[1].Value.([]byte)) != "fw-01" {
		t.Errorf("LLDP: %+v", pdus)
	}
	if small, _ := NewSwitch(4).Walk("1.0.8802.1.1.2.1.4.1.1.9"); len(small) != 3 { // port 4 has no link
		t.Errorf("small switch: %d neighbors, want 3", len(small))
	}
}
