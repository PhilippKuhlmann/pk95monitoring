package snmp

import (
	"testing"

	"pk95monitoring/internal/model"
)

// The storage list of a UTM 14.1: /data, /home and /root are one file system, so are /etc and
// /etc/machine-id; empty tmpfs mounts of the same size stay apart.
func TestMergeBindMounts(t *testing.T) {
	disk := func(path string, total, used float64) model.Check {
		return model.Check{Key: "snmp:storage:" + path, Facts: map[string]string{}, Metrics: []model.Metric{
			{Name: "disk.used", Instance: path, Value: used}, {Name: "disk.total", Instance: path, Value: total}}}
	}
	p := &poll{checks: []model.Check{
		{Key: "snmp:cpu"},
		disk("/rootdisk", 1e9, 1.5e8),
		disk("/etc/machine-id", 1e9, 7e6),
		disk("/mnt", 1e9, 0),
		disk("/etc", 1e9, 7e6),
		disk("/root", 3e10, 3.5e7),
		disk("/data", 3e10, 3.5e7),
		disk("/home", 3e10, 3.5e7),
		disk("/data/captiveportal", 1e9, 0),
	}}
	p.mergeBindMounts()
	var keys []string
	for _, ck := range p.checks {
		keys = append(keys, ck.Key)
	}
	want := []string{"snmp:cpu", "snmp:storage:/rootdisk", "snmp:storage:/mnt", "snmp:storage:/etc", "snmp:storage:/data", "snmp:storage:/data/captiveportal"}
	if len(keys) != len(want) {
		t.Fatalf("keys %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys %v, want %v", keys, want)
		}
	}
	if got := p.checks[3].Facts["Auch eingehängt unter"]; got != "/etc/machine-id" {
		t.Errorf("/etc also mounted at %q", got)
	}
	if got := p.checks[4].Facts["Auch eingehängt unter"]; got != "/root, /home" {
		t.Errorf("/data also mounted at %q", got)
	}
}
