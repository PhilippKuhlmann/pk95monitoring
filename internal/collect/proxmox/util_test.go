package proxmox

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"pk95monitoring/internal/model"
)

func TestFingerprintFormat(t *testing.T) {
	tests := []struct{ in, norm, format string }{
		{"aa:BB:0c", "aabb0c", "AA:BB:0C"},
		{" AABB0C ", "aabb0c", "AA:BB:0C"},
		{"aa bb 0c", "aabb0c", "AA:BB:0C"},
		{"", "", ""},
	}
	for _, tt := range tests {
		if got := NormalizeFingerprint(tt.in); got != tt.norm {
			t.Errorf("Normalize(%q) = %q", tt.in, got)
		}
		if got := FormatFingerprint(tt.in); got != tt.format {
			t.Errorf("Format(%q) = %q", tt.in, got)
		}
	}
}

func TestNumDecode(t *testing.T) {
	var v struct {
		A, B, C, D num
		W, X       optNum
	}
	if err := json.Unmarshal([]byte(`{"A":"0.52","B":3,"C":"N/A","D":null,"W":"N/A","X":97}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.A != 0.52 || v.B != 3 || v.C != 0 || v.D != 0 {
		t.Errorf("num: %+v", v)
	}
	if v.W.OK || !v.X.OK || v.X.V != 97 {
		t.Errorf("optNum: %+v %+v", v.W, v.X)
	}
}

func TestFormat(t *testing.T) {
	tests := []struct{ got, want string }{
		{fmtBytes(512), "512 B"},
		{fmtBytes(1536), "1,5 KiB"},
		{fmtBytes(94 << 30), "94,0 GiB"},
		{fmtBytes(830 << 30), "830 GiB"},
		{fmtPct(91.6), "92 %"},
		{fmtFloat(0.5), "0,50"},
		{fmtDuration(90), "1 Min."},
		{fmtDuration(5 * 3600), "5 Std."},
		{fmtDuration(30 * 3600), "1 Tag"},
		{fmtSince(41 * 86400), "seit 41 Tagen"},
		{fmtSince(30 * 3600), "seit 1 Tag"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("got %q, want %q", tt.got, tt.want)
		}
	}
	if round2(12.3456) != 12.35 || round2(-1.005) != -1 || round2(2e9+0.7) != 2e9 {
		t.Errorf("round2: %v %v %v", round2(12.3456), round2(-1.005), round2(2e9+0.7))
	}
}

func TestFailedGuests(t *testing.T) {
	lines := []taskLine{
		{T: "INFO: Starting Backup of VM 102 (qemu)"},
		{T: "ERROR: Backup of VM 103 failed - job failed with err -5"},
		{T: "INFO: Backup of VM 104 failed earlier (not an error line)"},
		{T: "ERROR: Backup of VM 201 failed - unable to open file"},
		{T: "ERROR: Backup of VM 103 failed - again"},
	}
	if got := failedGuests(lines); !reflect.DeepEqual(got, []string{"103", "201"}) {
		t.Errorf("failedGuests = %v", got)
	}
}

func TestPermMessage(t *testing.T) {
	if got := permMessage(&APIError{Status: 403, Message: "Permission check failed"}, "Sys.Audit"); got != "Keine Berechtigung (Sys.Audit)" {
		t.Errorf("403: %q", got)
	}
	if got := permMessage(errors.New("boom"), "x"); got != "Abfrage fehlgeschlagen: boom" {
		t.Errorf("other: %q", got)
	}
}

func TestDiskWearout(t *testing.T) {
	// The used life is the metric disk.wearout (its threshold warns); the native state is SMART.
	tests := []struct {
		d     disk
		state string
		wear  float64 // -1: no metric
	}{
		{disk{DevPath: "/dev/sda", Type: "ssd", Health: "PASSED", Wearout: optNum{V: 11, OK: true}}, "ok", 89},
		{disk{DevPath: "/dev/sda", Type: "ssd", Health: "PASSED", Wearout: optNum{V: 10, OK: true}}, "ok", 90},
		{disk{DevPath: "/dev/sda", Type: "ssd", Health: "FAILED", Wearout: optNum{V: 5, OK: true}}, "crit", 95},
		{disk{DevPath: "/dev/nvme0n1", Type: "nvme", Health: "OK"}, "ok", -1},
		{disk{DevPath: "/dev/sdb", Type: "hdd", Health: "UNKNOWN"}, "ok", -1},
		{disk{DevPath: "/dev/sdc", Type: "hdd", Health: "PREFAIL"}, "warn", -1},
	}
	for _, tt := range tests {
		got := diskChecks("pve1", []disk{tt.d}, nil)
		if len(got) != 1 || got[0].State != tt.state {
			t.Errorf("%+v: %+v", tt.d, got)
			continue
		}
		w, ok := metricValue(got[0], "disk.wearout")
		if (tt.wear < 0) == ok || (ok && w != tt.wear) {
			t.Errorf("%+v: wearout %v %v", tt.d, w, ok)
		}
	}
}

func metricValue(ch model.Check, name string) (float64, bool) {
	for _, m := range ch.Metrics {
		if m.Name == name {
			return m.Value, true
		}
	}
	return 0, false
}
