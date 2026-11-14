package redfish

import (
	"context"
	"strings"
	"testing"
	"time"

	"pk95monitoring/internal/fake/fakeredfish"
	"pk95monitoring/internal/model"
)

func setup(t *testing.T) (*fakeredfish.Server, *Collector, model.Device) {
	t.Helper()
	srv := fakeredfish.New()
	t.Cleanup(srv.Close)
	cfg := srv.Config()
	return srv, New(), model.Device{ID: "r1", Name: "R650", Type: model.TypeRedfish, Host: srv.Host(), Redfish: &cfg}
}

func byKey(list []model.Check) map[string]model.Check {
	m := map[string]model.Check{}
	for _, c := range list {
		m[c.Key] = c
	}
	return m
}

func count(c model.Check, name string) int {
	n := 0
	for _, m := range c.Metrics {
		if m.Name == name {
			n++
		}
	}
	return n
}

const raid = "RAID.Integrated.1-1"

func TestCollect(t *testing.T) {
	srv, c, d := setup(t)
	checks, err := c.Collect(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	m := byKey(checks)
	if checks[0].Key != "rf:system" {
		t.Errorf("system first: %s", checks[0].Key)
	}
	sys := m["rf:system"]
	if sys.State != model.StateOK || sys.Message != "Dell Inc. PowerEdge R650 · eingeschaltet" || sys.Facts["Service-Tag / SKU"] != "7XK2PQ3" ||
		sys.Facts["Controller"] != "15G Monolithic 7.10.70.00" || sys.Facts["Gesamtzustand"] != "Kritisch" {
		t.Errorf("system %+v", sys)
	}
	pw := m["rf:power"]
	if pw.State != model.StateCrit || !strings.Contains(pw.Message, "PS2 Status kritisch (kein Eingangsstrom") || !strings.Contains(pw.Message, "Redundanz verloren") ||
		count(pw, "power.watts") != 1 {
		t.Errorf("power %+v", pw)
	}
	if f := m["rf:fans"]; f.State != model.StateOK || f.Message != "6 Lüfter in Ordnung" || count(f, "fan.rpm") != 6 {
		t.Errorf("fans %+v", f)
	}
	if tp := m["rf:temps"]; tp.State != model.StateOK || count(tp, "temp") != 4 || !strings.HasPrefix(tp.Message, "Zuluft ") {
		t.Errorf("temps %+v", tp)
	}
	if mem := m["rf:memory"]; mem.State != model.StateOK || mem.Message != "256 GiB · Zustand OK" {
		t.Errorf("memory %+v", mem)
	}
	if cpu := m["rf:cpu"]; cpu.State != model.StateOK || !strings.HasPrefix(cpu.Message, "2 × Intel") {
		t.Errorf("cpu %+v", cpu)
	}
	if ctrl := m["rf:ctrl:"+raid]; ctrl.Name != "Controller PERC H755 Front" || ctrl.Message != "2 Volumes · 5 Platten" || ctrl.State != model.StateOK {
		t.Errorf("controller %+v", ctrl)
	}
	if _, ok := m["rf:ctrl:AHCI.Embedded.1-1"]; ok {
		t.Error("controller without drives listed")
	}
	if v := m["rf:vol:"+raid+":Disk.Virtual.1:"+raid]; v.Name != "Volume VMs (RAID5)" || v.State != model.StateOK || v.Facts["Größe"] != "5,8 TB" {
		t.Errorf("volume %+v", v)
	}
	bad := m["rf:drive:"+raid+":Disk.Bay.4:Enclosure.Internal.0-1:"+raid]
	if bad.State != model.StateWarn || !strings.HasPrefix(bad.Message, "Ausfall vorhergesagt") || len(bad.Metrics) != 1 || bad.Metrics[0].Value != 96 {
		t.Errorf("predicted drive %+v", bad)
	}
	if good := m["rf:drive:"+raid+":Disk.Bay.0:Enclosure.Internal.0-1:"+raid]; good.State != model.StateOK || good.Message != "OK · SSD 480 GB MZ7L3480HCHQAD3" {
		t.Errorf("drive %+v", good)
	}

	// power restored; the server switched off is critical
	srv.SetPSU2("OK")
	srv.SetPowerState("Off")
	checks, err = c.Collect(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	m = byKey(checks)
	if pw := m["rf:power"]; pw.State != model.StateOK || !strings.HasPrefix(pw.Message, "2 Netzteile in Ordnung") {
		t.Errorf("power restored %+v", pw)
	}
	if s := m["rf:system"]; s.State != model.StateCrit || !strings.Contains(s.Message, "ausgeschaltet") {
		t.Errorf("off %+v", s)
	}
}

// The overall health decides when no component check explains it.
func TestRollupWithoutComponent(t *testing.T) {
	sys := system{PowerState: "On", Status: status{Health: "Warning", HealthRollup: "Warning"}}
	if ch := systemCheck(serviceRoot{}, sys, "", []model.Check{{State: model.StateOK}}); ch.State != model.StateWarn || !strings.Contains(ch.Message, "Gesamtzustand laut Controller: Warnung") {
		t.Errorf("rollup %+v", ch)
	}
	if ch := systemCheck(serviceRoot{}, sys, "", []model.Check{{State: model.StateWarn}}); ch.State != model.StateOK {
		t.Errorf("explained rollup %+v", ch)
	}
}

func TestThresholdsAndRebuild(t *testing.T) {
	th := thermal{Temperatures: []tempSensor{
		{Name: "CPU1", ReadingCelsius: num{95, true}, UpperNonCrit: num{90, true}, UpperCrit: num{98, true}},
		{Name: "Inlet", ReadingCelsius: num{24, true}},
		{Name: "Absent", Status: status{State: "Absent"}},
	}}
	if ch := tempCheck(th); ch.State != model.StateWarn || ch.Message != "zu warm: CPU1 95 °C" || len(ch.Metrics) != 2 {
		t.Errorf("temps %+v", ch)
	}
	vols := []volume{{ID: "v", Name: "Daten", RAIDType: "RAID6", Status: status{State: "Enabled", Health: "Warning"},
		Operations: []operation{{OperationName: "Rebuild", PercentageComplete: num{37, true}}}}}
	cs := storageChecks(storageRes{ID: "s"}, vols, []drive{{ID: "d", Status: status{State: "Enabled", Health: "Critical"}}})
	if v := cs[1]; v.State != model.StateWarn || !strings.Contains(v.Message, "Wiederaufbau 37 %") {
		t.Errorf("rebuild %+v", v)
	}
	if dr := cs[2]; dr.State != model.StateCrit || dr.Message != "Platte ausgefallen" {
		t.Errorf("failed drive %+v", dr)
	}
}

func TestErrors(t *testing.T) {
	srv, c, d := setup(t)
	bad := *d.Redfish
	bad.Password = "falsch"
	d.Redfish = &bad
	if _, err := c.Collect(context.Background(), d); err == nil || !strings.Contains(err.Error(), "Anmeldung abgelehnt") {
		t.Errorf("wrong password: %v", err)
	}
	wrongFP := srv.Config()
	wrongFP.Fingerprint = strings.Repeat("ab", 32)
	if _, err := Test(context.Background(), srv.Host(), wrongFP); err == nil || !strings.Contains(err.Error(), "Fingerabdruck") {
		t.Errorf("fingerprint: %v", err)
	}
	res, err := Test(context.Background(), srv.Host(), srv.Config())
	if err != nil || res.Model != "PowerEdge R650" || res.Serial != "7XK2PQ3" || res.Power != "On" || res.Vendor != "Dell Inc." {
		t.Errorf("test %+v %v", res, err)
	}
}

// The storage tree is read once per storageTTL.
func TestStorageCache(t *testing.T) {
	_, c, d := setup(t)
	now := time.Now()
	c.now = func() time.Time { return now }
	if _, err := c.Collect(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	at := c.devs[d.ID].storageAt
	c.mu.Unlock()
	now = now.Add(time.Minute)
	if _, err := c.Collect(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	again := c.devs[d.ID].storageAt
	c.mu.Unlock()
	if !again.Equal(at) {
		t.Error("storage read again within the TTL")
	}
}
