package agent

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"

	"pk95monitoring/internal/model"
)

func TestSkipMount(t *testing.T) {
	for _, tc := range []struct {
		fstype, mount string
		skip          bool
	}{
		{"ext4", "/", false},
		{"xfs", "/var", false},
		{"zfs", "/rpool", false},
		{"vfat", "/boot/efi", false},
		{"NTFS", "C:", false},
		{"nfs4", "/mnt/backup", false},
		{"tmpfs", "/run", true},
		{"devtmpfs", "/dev", true},
		{"overlay", "/var/lib/docker/overlay2/x/merged", true},
		{"squashfs", "/snap/core/123", true},
		{"ext4", "/snap/foo", true},
		{"proc", "/proc", true},
		{"sysfs", "/sys", true},
		{"cgroup2", "/sys/fs/cgroup", true},
		{"autofs", "/proc/sys/fs/binfmt_misc", true},
		{"nsfs", "/run/netns/x", true},
		{"tracefs", "/sys/kernel/tracing", true},
		{"debugfs", "/sys/kernel/debug", true},
		{"fuse.lxcfs", "/var/lib/lxcfs", true},
		{"efivarfs", "/sys/firmware/efi/efivars", true},
		{"zfs", "/rpool/data/subvol-101-disk-0", true},
		{"ext4", "", true},
	} {
		if got := skipMount(tc.fstype, tc.mount); got != tc.skip {
			t.Errorf("skipMount(%q, %q) = %v, want %v", tc.fstype, tc.mount, got, tc.skip)
		}
	}
}

func TestFilterPartitionsDedupesByDevice(t *testing.T) {
	parts := []disk.PartitionStat{
		{Device: "/dev/sda1", Mountpoint: "/srv/bind", Fstype: "ext4"},
		{Device: "/dev/sda1", Mountpoint: "/", Fstype: "ext4"},
		{Device: "/dev/sdb1", Mountpoint: "/data", Fstype: "xfs"},
		{Device: "tmpfs", Mountpoint: "/run", Fstype: "tmpfs"},
		{Device: "C:", Mountpoint: "C:", Fstype: "NTFS"},
	}
	got := filterPartitions(parts)
	var mounts []string
	for _, p := range got {
		mounts = append(mounts, p.Mountpoint)
	}
	if strings.Join(mounts, ",") != "/,/data,C:" {
		t.Errorf("mounts %v", mounts)
	}
}

func TestWinMount(t *testing.T) {
	for in, want := range map[string]string{"c:": "C:", `D:\`: "D:", `C:\Mounts\Data\`: `C:\Mounts\Data`, "": ""} {
		if got := winMount(in); got != want {
			t.Errorf("winMount(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSkipIface(t *testing.T) {
	keep := []string{"eth0", "ens18", "enp3s0", "eno1", "vmbr0", "vmbr1", "bond0", "wlp2s0", "Ethernet", "Ethernet 2",
		"WLAN", "Local Area Connection", "vEthernet (External)", "en0"}
	skip := []string{"lo", "lo0", "veth1234", "docker0", "br-1a2b3c", "virbr0", "tap100i0", "fwbr100i0", "fwpr100p0",
		"fwln100i0", "tun0", "Loopback Pseudo-Interface 1", "isatap.{1234}", "Teredo Tunneling Pseudo-Interface",
		"Local Area Connection* 2", "utun3", "awdl0", ""}
	for _, n := range keep {
		if skipIface(n) {
			t.Errorf("%q skipped", n)
		}
	}
	for _, n := range skip {
		if !skipIface(n) {
			t.Errorf("%q kept", n)
		}
	}
}

func TestIgnoredServices(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"sppsvc", true},
		{"SPPSVC", true},
		{"GoogleUpdaterService128.0.6537.0", true},
		{"clr_optimization_v4.0.30319_64", true},
		{"MSSQLSERVER", false},
		{"custom", true},
		{"customer", false},
	} {
		if got := ignored(tc.name, defaultIgnoredServices, []string{"custom"}); got != tc.want {
			t.Errorf("ignored(%q) = %v", tc.name, got)
		}
	}
}

func TestCPUUsageAndRate(t *testing.T) {
	prev := cpu.TimesStat{User: 100, System: 50, Idle: 800, Iowait: 50}
	cur := cpu.TimesStat{User: 160, System: 70, Idle: 900, Iowait: 70}
	// delta total 200, idle+iowait delta 120 → 40 % busy
	if u, ok := cpuUsage(prev, cur); !ok || u < 39.99 || u > 40.01 {
		t.Errorf("cpuUsage = %v, %v", u, ok)
	}
	if _, ok := cpuUsage(cur, cur); ok {
		t.Error("zero interval accepted")
	}
	if r, ok := rate(1000, 3000, 2); !ok || r != 1000 {
		t.Errorf("rate = %v", r)
	}
	if _, ok := rate(3000, 1000, 2); ok {
		t.Error("counter wrap produced a rate")
	}
}

var validKey = map[string]bool{"system": true, "cpu": true, "mem": true, "swap": true, "systemd": true, "updates": true}

var metricUnits = map[string]string{
	"cpu.usage": "%", "cpu.load1": "", "mem.usage": "%", "mem.used": "B", "mem.total": "B", "swap.usage": "%",
	"disk.usage": "%", "disk.used": "B", "disk.total": "B", "net.rx": "B/s", "net.tx": "B/s", "uptime": "s",
	"procs": "", "services.failed": "", "updates.pending": "", "updates.security": "", "events.count": "",
}

func TestCollectCurrentOS(t *testing.T) {
	if testing.Short() {
		t.Skip("collects real system data")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := NewCollector()
	rep := c.Report(ctx, Config{})
	if rep.AgentID == "" || rep.Hostname == "" || rep.OS == "" || rep.Arch == "" || rep.Version != Version || rep.Time == 0 {
		t.Errorf("report header %+v", rep)
	}
	seen := map[string]bool{}
	hasDisk := false
	for _, ch := range rep.Checks {
		if seen[ch.Key] {
			t.Errorf("duplicate key %s", ch.Key)
		}
		seen[ch.Key] = true
		prefix, _, _ := strings.Cut(ch.Key, ":")
		if !validKey[ch.Key] && prefix != "disk" && prefix != "if" && prefix != "svc" {
			t.Errorf("invalid key %q", ch.Key)
		}
		if prefix == "disk" {
			hasDisk = true
		}
		if ch.Name == "" || ch.Group == "" {
			t.Errorf("%s: name/group missing", ch.Key)
		}
		if model.Rank(ch.State) == 1 && ch.State != model.StateUnknown {
			t.Errorf("%s: invalid state %q", ch.Key, ch.State)
		}
		for _, m := range ch.Metrics {
			unit, ok := metricUnits[m.Name]
			if !ok || unit != m.Unit {
				t.Errorf("%s: metric %s unit %q not in the contract", ch.Key, m.Name, m.Unit)
			}
		}
	}
	for _, k := range []string{"system", "cpu", "mem"} {
		if !seen[k] {
			t.Errorf("check %s missing (have %v)", k, seen)
		}
	}
	if !hasDisk {
		t.Error("no disk check")
	}
	// A second call reuses the counters and must not sleep the priming second again.
	start := time.Now()
	if checks := c.Collect(ctx, Config{}); len(checks) == 0 {
		t.Error("second collect empty")
	}
	if time.Since(start) > 900*time.Millisecond {
		t.Errorf("second collect took %v", time.Since(start))
	}
}

func TestPeriodic(t *testing.T) {
	var calls atomic.Int32
	fail := atomic.Bool{}
	now := time.Unix(1000, 0)
	p := newPeriodic(time.Hour, time.Minute, time.Second, func(context.Context) (int, error) {
		n := calls.Add(1)
		if fail.Load() {
			return 0, errors.New("boom")
		}
		return int(n), nil
	})
	var nowMu atomic.Int64
	nowMu.Store(now.Unix())
	p.now = func() time.Time { return time.Unix(nowMu.Load(), 0) }
	ctx := context.Background()

	if v, err, ok := p.Get(ctx, true); !ok || err != nil || v != 1 {
		t.Fatalf("first get: %v %v %v", v, err, ok)
	}
	if v, _, _ := p.Get(ctx, true); v != 1 || calls.Load() != 1 {
		t.Fatalf("cached value not reused: %v calls=%d", v, calls.Load())
	}
	// Due again, but the refresh fails: the last good value stays.
	nowMu.Add(3601)
	fail.Store(true)
	if v, err, _ := p.Get(ctx, true); v != 1 || err != nil {
		t.Fatalf("after failed refresh: %v %v", v, err)
	}
	// Retry after one minute succeeds.
	nowMu.Add(61)
	fail.Store(false)
	if v, _, _ := p.Get(ctx, true); v != 3 {
		t.Fatalf("after retry: %v", v)
	}
	// Invalidate refreshes at once although the value is not due.
	p.Invalidate()
	if v, _, _ := p.Get(ctx, true); v != 4 {
		t.Fatalf("after invalidate: %v", v)
	}

	// Background mode returns immediately without a value on the first call.
	block := make(chan struct{})
	slow := newPeriodic(time.Hour, time.Minute, time.Second, func(context.Context) (string, error) { <-block; return "x", nil })
	if _, _, ok := slow.Get(ctx, false); ok {
		t.Error("value before the first run finished")
	}
	close(block)
	if v, _, ok := slow.Get(ctx, true); !ok || v != "x" {
		t.Errorf("wait for running refresh: %q %v", v, ok)
	}
}
