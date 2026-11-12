package proxmox

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"pk95monitoring/internal/model"
)

func TestATASmart(t *testing.T) {
	var info smartInfo
	raw := `{"health":"PASSED","type":"ata","attributes":[
		{"id":"  5","name":"Reallocated_Sector_Ct","value":100,"threshold":10,"raw":"8","fail":"-"},
		{"id":"  9","name":"Power_On_Hours","value":70,"threshold":0,"raw":"26300","fail":"-"},
		{"id":" 12","name":"Power_Cycle_Count","value":100,"threshold":0,"raw":"41","fail":"-"},
		{"id":"194","name":"Temperature_Celsius","value":64,"threshold":0,"raw":"36 (Min/Max 22/45)","fail":"-"},
		{"id":"197","name":"Current_Pending_Sector","value":100,"threshold":0,"raw":2,"fail":"-"},
		{"id":"198","name":"Offline_Uncorrectable","value":100,"threshold":0,"raw":"1","fail":"-"},
		{"id":"199","name":"UDMA_CRC_Error_Count","value":200,"threshold":0,"raw":"3","fail":"-"},
		{"id":"  1","name":"Raw_Read_Error_Rate","value":1,"threshold":51,"raw":"123","fail":"FAILING_NOW"}]}`
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatal(err)
	}
	ch := diskChecks("pve3", []disk{{DevPath: "/dev/sdc", Type: "hdd", Health: "PASSED", Model: "WDC WD40EFRX"}},
		map[string]smartEntry{"/dev/sdc": {info: info}})[0]
	for name, want := range map[string]float64{"disk.temp": 36, "disk.reallocated": 8, "disk.pending": 2, "disk.uncorrectable": 1, "disk.crc_errors": 3} {
		if v, ok := metricValue(ch, name); !ok || v != want {
			t.Errorf("%s = %v %v, want %v", name, v, ok, want)
		}
	}
	if ch.State != model.StateCrit || !strings.Contains(ch.Message, "SMART-Wert unter Grenze: Raw Read Error Rate") ||
		!strings.Contains(ch.Message, "36 °C") || !strings.Contains(ch.Message, "2 ausstehende Sektoren") {
		t.Errorf("check %s %q", ch.State, ch.Message)
	}
	if ch.Facts["Betriebsstunden"] != "26.300 h (3,0 Jahre)" || ch.Facts["CRC-Fehler (Kabel)"] != "3" || ch.Facts["Einschaltvorgänge"] != "41" {
		t.Errorf("facts %v", ch.Facts)
	}
	if m := ch.Metrics[0]; m.Instance != "pve3/sdc" || m.Unit != "°C" {
		t.Errorf("metric %+v", m)
	}
}

func TestNVMeSmart(t *testing.T) {
	txt := `SMART/Health Information (NVMe Log 0x02)
Critical Warning:                   0x00
Temperature:                        41 Celsius
Available Spare:                    100%
Available Spare Threshold:          10%
Percentage Used:                    3%
Data Units Written:                 12,345,678 [6.32 TB]
Power On Hours:                     4,321
Unsafe Shutdowns:                   12
Media and Data Integrity Errors:    0
`
	d := disk{DevPath: "/dev/nvme0n1", Type: "nvme", Health: "PASSED", Wearout: optNum{V: 97, OK: true}}
	ch := diskChecks("pve1", []disk{d}, map[string]smartEntry{"/dev/nvme0n1": {info: smartInfo{Health: "PASSED", Type: "text", Text: txt}}})[0]
	for name, want := range map[string]float64{"disk.temp": 41, "disk.spare": 100, "disk.wearout": 3, "disk.media_errors": 0} {
		if v, ok := metricValue(ch, name); !ok || v != want {
			t.Errorf("%s = %v %v, want %v", name, v, ok, want)
		}
	}
	if ch.State != model.StateOK || ch.Facts["Geschrieben"] != "6.32 TB" || ch.Facts["Betriebsstunden"] != "4.321 h (180 Tage)" || ch.Facts["Unsichere Abschaltungen"] != "12" {
		t.Errorf("check %s %q %v", ch.State, ch.Message, ch.Facts)
	}
	// A critical warning bit is crit with its meaning.
	bad := strings.Replace(txt, "0x00", "0x05", 1)
	ch = diskChecks("pve1", []disk{d}, map[string]smartEntry{"/dev/nvme0n1": {info: smartInfo{Text: bad}}})[0]
	if ch.State != model.StateCrit || !strings.Contains(ch.Message, "NVMe-Warnung: Reserve unter Grenzwert, Zuverlässigkeit eingeschränkt") {
		t.Errorf("critical warning: %s %q", ch.State, ch.Message)
	}
}

func TestParseScan(t *testing.T) {
	loc := time.UTC
	tests := []struct {
		in      string
		kind    string
		running bool
		at      string
		errs    int
		done    float64
	}{
		{"scrub repaired 0B in 00:01:23 with 0 errors on Sun Nov  9 00:24:24 2025", "scrub", false, "2025-11-09 00:24", 0, 0},
		{"scrub repaired 4K in 01:01:23 with 2 errors on Sun Oct 12 03:00:01 2025", "scrub", false, "2025-10-12 03:00", 2, 0},
		{"scrub in progress since Sun Nov  9 00:24:01 2025\n\t1.23T scanned at 1.1G/s, 45.6% done, 01:23:45 to go", "scrub", true, "2025-11-09 00:24", 0, 45.6},
		{"resilver in progress since Mon Nov 10 08:00:00 2025\n\t300G resilvered, 12.5% done, no estimated completion time", "resilver", true, "2025-11-10 08:00", 0, 12.5},
		{"resilvered 1.2T in 03:21:00 with 0 errors on Mon Nov 10 11:21:00 2025", "resilver", false, "2025-11-10 11:21", 0, 0},
		{"none requested", "", false, "", 0, 0},
		{"scrub canceled on Sun Nov  9 01:00:00 2025", "", false, "", 0, 0},
	}
	for _, tt := range tests {
		si := parseScan(tt.in, loc)
		at := ""
		if !si.at.IsZero() {
			at = si.at.Format("2006-01-02 15:04")
		}
		if si.kind != tt.kind || si.running != tt.running || at != tt.at || si.errors != tt.errs || si.done != tt.done {
			t.Errorf("parseScan(%q) = %+v (at %s)", tt.in, si, at)
		}
	}
}

func TestZFSDetail(t *testing.T) {
	now := time.Date(2025, 11, 20, 12, 0, 0, 0, time.Local)
	var d zfsDetail
	raw := `{"name":"tank","state":"DEGRADED","status":"One or more devices has been removed.","errors":"No known data errors",
		"scan":"scrub repaired 0B in 05:00:00 with 0 errors on Sun Oct 12 03:00:01 2025",
		"children":[{"name":"raidz1-0","state":"DEGRADED","read":0,"write":0,"cksum":0,"children":[
			{"name":"sdb","state":"ONLINE","read":0,"write":0,"cksum":"1.20K"},
			{"name":"sdc","state":"FAULTED","read":"3","write":0,"cksum":0,"msg":"too many errors"}]}]}`
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	pools := []zfsPool{{Name: "tank", Health: "DEGRADED", Size: 1000, Alloc: 830, Free: 170, Frag: 14}}
	ch := zfsChecks("pve3", pools, map[string]zfsDetail{"tank": d}, now)[0]
	if ch.State != model.StateWarn || !strings.Contains(ch.Message, "raidz1-0 DEGRADED, sdc FAULTED") || !strings.Contains(ch.Message, "1.203 Lese-/Schreib-/Prüfsummenfehler") {
		t.Errorf("check %s %q", ch.State, ch.Message)
	}
	if v, _ := metricValue(ch, "zfs.errors"); v != 1203 {
		t.Errorf("errors %v", v)
	}
	if v, _ := metricValue(ch, "zfs.usage"); v != 83 {
		t.Errorf("usage %v", v)
	}
	if v, ok := metricValue(ch, "zfs.scrub_age"); !ok || v < 39 || v > 40 {
		t.Errorf("scrub age %v %v", v, ok)
	}
	if ch.Facts["Letzter Scrub"] != "12.10.2025 03:00" || ch.Facts["Hinweis"] == "" {
		t.Errorf("facts %v", ch.Facts)
	}
	// Data errors are crit.
	d.Errors = "2 data errors, use '-v' for a list"
	if ch := zfsChecks("pve3", pools, map[string]zfsDetail{"tank": d}, now)[0]; ch.State != model.StateCrit || !strings.Contains(ch.Message, "Datenfehler: 2 data errors") {
		t.Errorf("data errors %s %q", ch.State, ch.Message)
	}
	// Without details (older PVE, no permission) the pool check stays as before.
	if ch := zfsChecks("pve3", pools, nil, now)[0]; ch.State != model.StateWarn || strings.Contains(ch.Message, "Fehler") {
		t.Errorf("no details %s %q", ch.State, ch.Message)
	}
}

func TestReplicationHACephCert(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	jobs := []replJob{
		{ID: "105-0", Guest: 105, Target: "pve1", LastSync: num(now.Add(-10 * time.Minute).Unix()), Duration: 4, Schedule: "*/15"},
		{ID: "100-0", Guest: 100, Target: "pve2", LastSync: num(now.Add(-2 * time.Hour).Unix()), FailCount: 3, Error: "storage 'local-zfs' not available"},
		{ID: "101-0", Guest: 101, Target: "pve2", Disable: 1},
	}
	out := replicationChecks("pve3", jobs, map[string]string{"100": "dc01", "105": "sql01"}, now)
	if len(out) != 3 || out[0].Key != "pve:repl:100-0" || out[0].State != model.StateCrit || out[0].Name != "Replikation 100 dc01 → pve2" ||
		!strings.Contains(out[0].Message, "fehlgeschlagen (3×): storage 'local-zfs' not available") {
		t.Fatalf("failed job %+v", out[0])
	}
	if out[1].State != model.StateOK || out[1].Message != "deaktiviert" || len(out[1].Metrics) != 0 {
		t.Errorf("disabled job %+v", out[1])
	}
	if out[2].State != model.StateOK || !strings.HasPrefix(out[2].Message, "zuletzt vor ") || out[2].Metrics[0].Value != 10 {
		t.Errorf("ok job %+v", out[2])
	}

	if _, ok := haCheck([]haEntry{{Type: "quorum", Status: "OK"}}); ok {
		t.Error("HA check without resources")
	}
	ha, ok := haCheck([]haEntry{{Type: "quorum", Status: "OK"}, {Type: "service", SID: "vm:100", State: "started"}, {Type: "service", SID: "ct:200", State: "error"}})
	if !ok || ha.State != model.StateCrit || ha.Message != "ct:200 Fehler" {
		t.Errorf("ha %+v", ha)
	}

	var cs cephStatus
	_ = json.Unmarshal([]byte(`{"health":{"status":"HEALTH_WARN","checks":{"OSD_DOWN":{"severity":"HEALTH_WARN","summary":{"message":"1 osds down"}}}},"pgmap":{"bytes_used":250,"bytes_total":1000}}`), &cs)
	if ch := cephCheck(cs); ch.State != model.StateWarn || ch.Message != "HEALTH_WARN · 1 osds down · 25 % belegt" {
		t.Errorf("ceph %+v", ch)
	}

	certs := []certInfo{{Filename: "pve-root-ca.pem", NotAfter: num(now.Add(3650 * 24 * time.Hour).Unix())},
		{Filename: "pve-ssl.pem", NotAfter: num(now.Add(700 * 24 * time.Hour).Unix())},
		{Filename: "pveproxy-ssl.pem", NotAfter: num(now.Add(20 * 24 * time.Hour).Unix()), Issuer: "/C=US/O=Let's Encrypt/CN=R11", SAN: []string{"pve1.example.org"}}}
	ch, ok := certCheck("pve1", certs, now)
	if !ok || ch.Facts["Datei"] != "pveproxy-ssl.pem" || ch.Metrics[0].Name != "cert.days" || ch.Metrics[0].Value != 20 || ch.Facts["Namen"] != "pve1.example.org" {
		t.Errorf("cert %+v", ch)
	}
	if _, ok := certCheck("pve1", nil, now); ok {
		t.Error("cert check without certificates")
	}
}

func TestCollectHealth(t *testing.T) {
	srv, c, d := setup(t)
	m := collect(t, c, d)

	// SMART: an HDD with pending sectors (the threshold warns, the native state is SMART), NVMe values.
	sdc := want(t, m, "pve:disk:pve3:sdc", model.StateOK, "", "")
	if v, _ := metricValue(sdc, "disk.pending"); v != 2 {
		t.Errorf("pending %v", sdc.Metrics)
	}
	if !strings.Contains(sdc.Message, "2 ausstehende Sektoren") || sdc.Facts["Betriebsstunden"] == "" {
		t.Errorf("sdc %q %v", sdc.Message, sdc.Facts)
	}
	nv := want(t, m, "pve:disk:pve1:nvme0n1", model.StateOK, "", "")
	if v, ok := metricValue(nv, "disk.temp"); !ok || v <= 0 {
		t.Errorf("nvme temp %v", nv.Metrics)
	}
	if v, _ := metricValue(nv, "disk.spare"); v != 100 {
		t.Errorf("nvme spare %v", nv.Metrics)
	}
	// ZFS details, replication, HA, certificates, IO delay; no Ceph without Ceph.
	tank := want(t, m, "pve:zfs:pve3:tank", model.StateOK, "", "")
	if v, ok := metricValue(tank, "zfs.scrub_age"); !ok || v < 19.9 || v > 20.1 {
		t.Errorf("tank scrub age %v", tank.Metrics)
	}
	if v, ok := metricValue(tank, "zfs.errors"); !ok || v != 0 {
		t.Errorf("tank errors %v", tank.Metrics)
	}
	want(t, m, "pve:repl:100-0", model.StateOK, "Replikation 100 dc01 → pve2", "Replikation")
	want(t, m, "pve:repl:105-0", model.StateOK, "", "")
	if ha := want(t, m, "pve:ha", model.StateOK, "Hochverfügbarkeit", "Cluster"); ha.Message != "3 Ressourcen, alle in Ordnung" {
		t.Errorf("ha %q", ha.Message)
	}
	if _, ok := m["pve:ceph"]; ok {
		t.Error("Ceph check without Ceph")
	}
	if cert := want(t, m, "pve:cert:pve1", model.StateOK, "Zertifikat pve1", "Zertifikate"); cert.Facts["Datei"] != "pveproxy-ssl.pem" {
		t.Errorf("cert %v", cert.Facts)
	}
	if _, ok := metricValue(m["pve:node:pve1"], "cpu.iowait"); !ok {
		t.Error("no IO delay")
	}

	// Problems.
	srv.SetVdevFaulted("pve3", "tank", "sdd", 1200)
	srv.SetReplicationFailed("100-0", "storage 'local-zfs' does not exist")
	srv.SetHAState("ct:201", "error")
	srv.SetCephHealth("HEALTH_WARN", "1 osds down")
	srv.SetIOWait("pve2", 0.31)
	srv.SetDiskSMART("pve3", "sdb", 66, 0, 0)
	m = collect(t, c, d)
	if tank := want(t, m, "pve:zfs:pve3:tank", model.StateWarn, "", ""); !strings.Contains(tank.Message, "sdd FAULTED") || !strings.Contains(tank.Message, "1.200 Lese-") {
		t.Errorf("tank %q", tank.Message)
	}
	if r := want(t, m, "pve:repl:100-0", model.StateCrit, "", ""); !strings.Contains(r.Message, "storage 'local-zfs' does not exist") {
		t.Errorf("repl %q", r.Message)
	}
	want(t, m, "pve:ha", model.StateCrit, "", "")
	want(t, m, "pve:ceph", model.StateWarn, "Ceph", "Speicher")
	if v, _ := metricValue(m["pve:node:pve2"], "cpu.iowait"); v != 31 {
		t.Errorf("iowait %v", v)
	}
	// SMART is cached: the new temperature shows after smartTTL.
	if v, _ := metricValue(m["pve:disk:pve3:sdb"], "disk.temp"); v == 66 {
		t.Error("SMART not cached")
	}
	srv.Advance(smartTTL + time.Minute)
	m = collect(t, c, d)
	if v, _ := metricValue(m["pve:disk:pve3:sdb"], "disk.temp"); v != 66 {
		t.Errorf("SMART after TTL: %v", v)
	}
}
