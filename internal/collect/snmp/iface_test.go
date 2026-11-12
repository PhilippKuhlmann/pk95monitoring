package snmp_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"pk95monitoring/internal/fake/fakesnmp"
	"pk95monitoring/internal/model"
)

// A port that goes down has no traffic metrics; when it comes back the first rate covers
// only the last poll interval (no average over the outage, no spike).
func TestPortDownAndBackRates(t *testing.T) {
	sw := fakesnmp.NewSwitch(8)
	c, clk := setup(map[string]*fakesnmp.Device{"sw": sw})
	d := device("sw", model.SNMPConfig{})
	const hcIn2 = "1.3.6.1.2.1.31.1.1.1.6.2"

	sw.Set(hcIn2, gosnmp.Counter64, uint64(1_000_000))
	collect(t, c, d)
	clk.advance(60 * time.Second)
	sw.Set(hcIn2, gosnmp.Counter64, uint64(1_060_000))
	m := collect(t, c, d)
	if rx, ok := metric(mustCheck(t, m, "snmp:if:Gi1/0/2"), "if.rx"); !ok || rx.Value != 1000 {
		t.Fatalf("rate while up = %+v (ok %v)", rx, ok)
	}

	sw.SetPortUp(2, false)
	for range 10 { // ten minutes without link
		clk.advance(60 * time.Second)
		m = collect(t, c, d)
		ck := mustCheck(t, m, "snmp:if:Gi1/0/2")
		if ck.State != model.StateOK || ck.Message != "Kein Link" || len(ck.Metrics) != 0 {
			t.Fatalf("port down: %+v", ck)
		}
	}

	// back up: 60 kB within the last minute
	sw.SetPortUp(2, true)
	clk.advance(60 * time.Second)
	sw.Set(hcIn2, gosnmp.Counter64, uint64(1_060_000+60_000))
	m = collect(t, c, d)
	ck := mustCheck(t, m, "snmp:if:Gi1/0/2")
	if rx, ok := metric(ck, "if.rx"); !ok || rx.Value != 1000 {
		t.Errorf("first rate after the outage = %+v (ok %v), want 1000 over the last minute", rx, ok)
	}
	if !strings.HasPrefix(ck.Message, "Verbunden") {
		t.Errorf("port back up: %+v", ck)
	}
}

// A counter that jumped while the port was down (e.g. reset and re-counted by the device)
// must not show up as one huge rate after the link comes back.
func TestPortBackNoSpike(t *testing.T) {
	sw := fakesnmp.NewSwitch(8)
	c, clk := setup(map[string]*fakesnmp.Device{"sw": sw})
	d := device("sw", model.SNMPConfig{})
	const hcIn2 = "1.3.6.1.2.1.31.1.1.1.6.2"

	sw.Set(hcIn2, gosnmp.Counter64, uint64(1_000))
	collect(t, c, d)
	sw.SetPortUp(2, false)
	clk.advance(60 * time.Second)
	sw.Set(hcIn2, gosnmp.Counter64, uint64(50_000_000)) // moved while down
	collect(t, c, d)
	sw.SetPortUp(2, true)
	clk.advance(60 * time.Second)
	sw.Set(hcIn2, gosnmp.Counter64, uint64(50_006_000))
	m := collect(t, c, d)
	if rx, ok := metric(mustCheck(t, m, "snmp:if:Gi1/0/2"), "if.rx"); !ok || rx.Value != 100 {
		t.Errorf("rate after the outage = %+v (ok %v), want 100", rx, ok)
	}
}

func TestUnwatchedOddStates(t *testing.T) {
	sw := fakesnmp.NewSwitch(8)
	const oper = "1.3.6.1.2.1.2.2.1.8."
	sw.Set(oper+"2", gosnmp.Integer, 5) // dormant
	sw.Set(oper+"3", gosnmp.Integer, 3) // testing
	sw.Set(oper+"4", gosnmp.Integer, 4) // unknown
	sw.Set(oper+"5", gosnmp.Integer, 6) // notPresent
	c, _ := setup(map[string]*fakesnmp.Device{"sw": sw})

	m := collect(t, c, device("sw", model.SNMPConfig{}))
	for k, want := range map[string]string{"Gi1/0/2": "Ruhend", "Gi1/0/3": "Testmodus", "Gi1/0/4": "Status unbekannt", "Gi1/0/5": "Kein Link"} {
		if ck := mustCheck(t, m, "snmp:if:"+k); ck.State != model.StateOK || ck.Message != want || len(ck.Metrics) != 0 {
			t.Errorf("%s: %+v, want ok %q", k, ck, want)
		}
	}
	if ck := mustCheck(t, m, "snmp:if:Gi1/0/2"); ck.Facts["Status"] != "ruhend" {
		t.Errorf("status fact: %+v", ck.Facts)
	}

	m = collect(t, c, device("sw", model.SNMPConfig{Interfaces: []string{"Gi1/0/2", "Gi1/0/3", "Gi1/0/4", "Gi1/0/5"}}))
	for k, want := range map[string]string{"Gi1/0/2": model.StateCrit, "Gi1/0/3": model.StateCrit, "Gi1/0/4": model.StateUnknown, "Gi1/0/5": model.StateCrit} {
		if ck := mustCheck(t, m, "snmp:if:"+k); ck.State != want {
			t.Errorf("watched %s: %+v, want %s", k, ck, want)
		}
	}
}

func TestInterfaceCap(t *testing.T) {
	sw := fakesnmp.NewSwitch(600) // 600 access ports, 2 uplinks, vlan1, loopback
	c, _ := setup(map[string]*fakesnmp.Device{"sw": sw})

	m := collect(t, c, device("sw", model.SNMPConfig{Interfaces: []string{"Te1/1/2"}}))
	n := 0
	for k := range m {
		if strings.HasPrefix(k, "snmp:if:") {
			n++
		}
	}
	if n != 513 { // 512 plus the watched uplink beyond the cap
		t.Errorf("got %d interface checks, want 513", n)
	}
	mustCheck(t, m, "snmp:if:Gi1/0/512")
	if _, ok := m["snmp:if:Gi1/0/513"]; ok {
		t.Error("port 513 is beyond the cap")
	}
	if ck := mustCheck(t, m, "snmp:if:Te1/1/2"); ck.State != model.StateOK || ck.Facts["Alarm bei Link-Ausfall"] != "ja" {
		t.Errorf("watched port beyond the cap: %+v", ck)
	}
	sys := mustCheck(t, m, "snmp:system")
	if want := fmt.Sprintf("512 von %d erfasst (Obergrenze 512)", 602); sys.Facts["Schnittstellen"] != want || sys.State != model.StateOK {
		t.Errorf("system: state %s, fact %q, want %q", sys.State, sys.Facts["Schnittstellen"], want)
	}
}
