package snmp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"pk95monitoring/internal/collect/snmp"
	"pk95monitoring/internal/fake/fakesnmp"
	"pk95monitoring/internal/model"
)

// hideRow removes one interface from ifTable (and from ifXTable when x is set).
func hideRow(d *fakesnmp.Device, index string, x bool) {
	for _, col := range []string{"2", "3", "5", "7", "8", "10", "14", "16", "20"} {
		d.Hide("1.3.6.1.2.1.2.2.1." + col + "." + index)
	}
	if x {
		for _, col := range []string{"1", "6", "10", "15", "18"} {
			d.Hide("1.3.6.1.2.1.31.1.1.1." + col + "." + index)
		}
	}
}

func TestWatchedPortKeyStaysStable(t *testing.T) {
	sw := fakesnmp.NewSwitch(24)
	c, _ := setup(map[string]*fakesnmp.Device{"sw": sw})
	d := device("sw", model.SNMPConfig{Interfaces: []string{"gi1/0/2", "GigabitEthernet1/0/20", "Gi1/0/5"}})
	collect(t, c, d) // resolves the configured names to ifName

	// the interface table times out: the watched ports keep their keys
	dial := c.Dial
	c.Dial = func(ctx context.Context, host string, cfg model.SNMPConfig, to time.Duration) (snmp.Session, error) {
		s, err := dial(ctx, host, cfg, to)
		if err != nil {
			return nil, err
		}
		return flaky{Session: s, fail: "1.3.6.1.2.1.2.2"}, nil
	}
	m := collect(t, c, d)
	for _, k := range []string{"snmp:if:Gi1/0/2", "snmp:if:Gi1/0/20", "snmp:if:Gi1/0/5"} {
		if ck := mustCheck(t, m, k); ck.State != model.StateUnknown || ck.Message != "Abfrage fehlgeschlagen" {
			t.Errorf("%s: %+v", k, ck)
		}
	}
	if _, ok := m["snmp:if:GigabitEthernet1/0/20"]; ok {
		t.Error("key must not switch to the configured ifDescr")
	}
	c.Dial = dial

	// port 20 vanished from both tables, port 5 only from ifTable (no link status)
	hideRow(sw, "20", true)
	hideRow(sw, "5", false)
	m = collect(t, c, d)
	if ck := mustCheck(t, m, "snmp:if:Gi1/0/20"); ck.State != model.StateUnknown || ck.Message != "Schnittstelle nicht gefunden" {
		t.Errorf("vanished port: %+v", ck)
	}
	if ck := mustCheck(t, m, "snmp:if:Gi1/0/5"); ck.State != model.StateUnknown || ck.Message != "Linkstatus nicht lesbar" {
		t.Errorf("port without ifOperStatus: %+v", ck)
	}
	if ck := mustCheck(t, m, "snmp:if:Gi1/0/2"); ck.State != model.StateOK {
		t.Errorf("port 2: %+v", ck)
	}
}

// counting counts the requests that reach the device and makes it unreachable when a
// walk below cut starts.
type counting struct {
	snmp.Session
	dev   *fakesnmp.Device
	cut   string
	calls int
}

func (c *counting) Get(oids []string) ([]gosnmp.SnmpPDU, error) {
	c.calls++
	return c.Session.Get(oids)
}

func (c *counting) Walk(root string) ([]gosnmp.SnmpPDU, error) {
	c.calls++
	if strings.HasPrefix(strings.TrimPrefix(root, "."), c.cut) {
		c.dev.SetReachable(false)
	}
	return c.Session.Walk(root)
}

func TestDeviceDiesDuringPoll(t *testing.T) {
	sw := fakesnmp.NewSwitch(8)
	c, _ := setup(map[string]*fakesnmp.Device{"sw": sw})
	var cs *counting
	dial := c.Dial
	c.Dial = func(ctx context.Context, host string, cfg model.SNMPConfig, to time.Duration) (snmp.Session, error) {
		s, err := dial(ctx, host, cfg, to)
		if err != nil {
			return nil, err
		}
		cs = &counting{Session: s, dev: sw, cut: "1.3.6.1.2.1.2.2"}
		return cs, nil
	}
	m := collect(t, c, device("sw", model.SNMPConfig{Interfaces: []string{"Gi1/0/1"},
		OIDs: []model.CustomOID{{OID: "1.3.6.1.2.1.1.7.0", Name: "Dienste"}}}))
	// system GET, the failing walk and one probe; everything else is skipped
	if cs.calls != 3 {
		t.Errorf("%d requests reached the dead device, want 3", cs.calls)
	}
	if ck := mustCheck(t, m, "snmp:system"); ck.State != model.StateUnknown {
		t.Errorf("system: %+v", ck)
	}
	if ck := mustCheck(t, m, "snmp:custom:Dienste"); ck.State != model.StateUnknown {
		t.Errorf("custom: %+v", ck)
	}

	// a walk that times out while the device still answers does not stop the poll
	sw.SetReachable(true)
	c.Dial = func(ctx context.Context, host string, cfg model.SNMPConfig, to time.Duration) (snmp.Session, error) {
		s, err := dial(ctx, host, cfg, to)
		if err != nil {
			return nil, err
		}
		return flaky{Session: s, fail: "1.3.6.1.2.1.2.2"}, nil
	}
	m = collect(t, c, device("sw", model.SNMPConfig{Profile: "switch"}))
	mustCheck(t, m, "snmp:cpu")
}

func TestAutoProfileSurvivesFailedDetection(t *testing.T) {
	prn := fakesnmp.NewPrinter()
	c, _ := setup(map[string]*fakesnmp.Device{"prn": prn})
	d := device("prn", model.SNMPConfig{})
	if sys := mustCheck(t, collect(t, c, d), "snmp:system"); sys.Facts["Profil"] != "Drucker" {
		t.Fatalf("first poll: %+v", sys.Facts)
	}
	dial := c.Dial
	c.Dial = func(ctx context.Context, host string, cfg model.SNMPConfig, to time.Duration) (snmp.Session, error) {
		s, err := dial(ctx, host, cfg, to)
		if err != nil {
			return nil, err
		}
		return flaky{Session: s, fail: "1.3.6.1.2.1.43.5"}, nil
	}
	m := collect(t, c, d)
	if sys := mustCheck(t, m, "snmp:system"); sys.Facts["Profil"] != "Drucker" {
		t.Errorf("profile after a failed detection: %+v", sys.Facts)
	}
	mustCheck(t, m, "snmp:printer")
	mustCheck(t, m, "snmp:toner:Black Toner")
}
