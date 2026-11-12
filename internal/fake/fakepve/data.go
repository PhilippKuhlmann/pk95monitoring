package fakepve

import (
	"math"
	"math/rand/v2"
	"time"
)

const (
	gib = 1 << 30
	tib = 1 << 40
)

type node struct {
	name, ip, cpuModel string
	cpus, sockets      int
	mem, root          float64 // bytes
	cpuBase, memBase   float64 // fractions
	rootPct            float64
	online             bool
	bootAt             time.Time
	phase              float64
}

type guest struct {
	vmid             int
	name, node, typ  string // typ qemu or lxc
	template, onboot bool
	status           string
	cores            int
	maxmem, maxdisk  float64
	cpuBase, memBase float64
	net, io          float64 // average B/s (in/out, read/write)
	startedAt        time.Time
	phase            float64
}

type storage struct {
	name, node, typ, content string
	shared                   bool
	total, pct               float64
	fixed                    bool // set by SetStorageUsage: no wobble
	phase                    float64
}

type pool struct {
	node, name, health string
	size, allocPct     float64
	frag               int
}

type disk struct {
	node, dev, model, serial, typ, health string
	size                                  float64
	wearout                               int // remaining life in percent, -1 = N/A
}

type aptPkg struct {
	Package    string `json:"Package"`
	Title      string `json:"Title"`
	Version    string `json:"Version"`
	OldVersion string `json:"OldVersion"`
	Origin     string `json:"Origin"`
	Priority   string `json:"Priority"`
	Section    string `json:"Section"`
}

// seed fills the demo cluster "demo": 3 nodes, 13 guests (one template), storages, ZFS pools,
// disks and apt updates. now is the fake clock at creation.
func (s *Server) seed(now time.Time) {
	day := 24 * time.Hour
	s.nodes = []*node{
		{name: "pve1", ip: "10.0.10.11", cpuModel: "AMD EPYC 4344P 8-Core Processor", cpus: 16, sockets: 1, mem: 128 * gib, root: 94 * gib, cpuBase: 0.18, memBase: 0.62, rootPct: 0.21, online: true, bootAt: now.Add(-41 * day), phase: 0.3},
		{name: "pve2", ip: "10.0.10.12", cpuModel: "AMD EPYC 4344P 8-Core Processor", cpus: 16, sockets: 1, mem: 128 * gib, root: 94 * gib, cpuBase: 0.22, memBase: 0.55, rootPct: 0.24, online: true, bootAt: now.Add(-41 * day), phase: 1.7},
		{name: "pve3", ip: "10.0.10.13", cpuModel: "Intel(R) Xeon(R) E-2388G CPU @ 3.20GHz", cpus: 16, sockets: 1, mem: 64 * gib, root: 94 * gib, cpuBase: 0.14, memBase: 0.71, rootPct: 0.33, online: true, bootAt: now.Add(-12 * day), phase: 2.9},
	}
	g := func(id int, name, nodeName, typ string, cores int, mem, dsk, cpu, memPct, net, io float64, up time.Duration) *guest {
		return &guest{vmid: id, name: name, node: nodeName, typ: typ, onboot: true, status: "running", cores: cores,
			maxmem: mem, maxdisk: dsk, cpuBase: cpu, memBase: memPct, net: net, io: io,
			startedAt: now.Add(-up), phase: float64(id%17) * 0.37}
	}
	s.guests = []*guest{
		g(100, "dc01", "pve1", "qemu", 4, 8*gib, 100*gib, 0.06, 0.55, 40e3, 120e3, 12*day),
		g(101, "fileserver", "pve1", "qemu", 4, 8*gib, 2*tib, 0.04, 0.45, 2.5e6, 4e6, 41*day),
		g(102, "mail", "pve2", "qemu", 4, 16*gib, 500*gib, 0.12, 0.70, 600e3, 900e3, 41*day),
		g(103, "nextcloud", "pve2", "qemu", 4, 8*gib, 1*tib, 0.10, 0.60, 1.2e6, 1.5e6, 9*day),
		g(104, "ts01", "pve1", "qemu", 8, 32*gib, 200*gib, 0.25, 0.65, 800e3, 2e6, 6*day),
		g(105, "sql01", "pve3", "qemu", 8, 32*gib, 300*gib, 0.18, 0.80, 400e3, 3e6, 12*day),
		g(106, "testvm", "pve3", "qemu", 2, 4*gib, 32*gib, 0, 0, 0, 0, 0),
		g(107, "erp", "pve2", "qemu", 4, 16*gib, 200*gib, 0, 0, 0, 0, 0),
		g(9000, "debian12-template", "pve3", "qemu", 2, 2*gib, 16*gib, 0, 0, 0, 0, 0),
		g(200, "unifi", "pve1", "lxc", 2, 2*gib, 16*gib, 0.03, 0.50, 60e3, 30e3, 41*day),
		g(201, "pihole", "pve2", "lxc", 1, 512<<20, 8*gib, 0.01, 0.30, 20e3, 5e3, 41*day),
		g(202, "grafana", "pve3", "lxc", 2, 2*gib, 20*gib, 0.05, 0.40, 90e3, 150e3, 12*day),
		g(203, "proxy", "pve3", "lxc", 1, 1*gib, 8*gib, 0.02, 0.25, 700e3, 10e3, 12*day),
	}
	for _, x := range s.guests {
		switch x.vmid {
		case 106: // stopped test VM without "Start at boot"
			x.status, x.onboot = "stopped", false
		case 107: // stopped although it should run
			x.status = "stopped"
		case 9000:
			x.status, x.onboot, x.template = "stopped", false, true
		}
	}
	for i, n := range s.nodes {
		ph := float64(i) * 0.9
		s.storages = append(s.storages,
			&storage{name: "local", node: n.name, typ: "dir", content: "iso,vztmpl,backup", total: 94 * gib, pct: 0.22 + 0.05*float64(i), phase: ph},
			&storage{name: "local-lvm", node: n.name, typ: "lvmthin", content: "images,rootdir", total: 1.7 * tib, pct: 0.48 + 0.08*float64(i), phase: ph + 0.4},
			&storage{name: "backup", node: n.name, typ: "nfs", content: "backup", shared: true, total: 16 * tib, pct: 0.63, phase: 1.1},
		)
		s.pools = append(s.pools, &pool{node: n.name, name: "rpool", health: "ONLINE", size: 1.8 * tib, allocPct: 0.24 + 0.04*float64(i), frag: 6 + 3*i})
	}
	s.storages = append(s.storages, &storage{name: "tank", node: "pve3", typ: "zfspool", content: "images,rootdir", total: 10.5 * tib, pct: 0.57, phase: 2.2})
	s.pools = append(s.pools, &pool{node: "pve3", name: "tank", health: "ONLINE", size: 14.5 * tib, allocPct: 0.57, frag: 14})

	s.disks = []*disk{
		{node: "pve1", dev: "nvme0n1", model: "Samsung SSD 980 PRO 2TB", serial: "S6B0NL0W412345K", typ: "nvme", health: "PASSED", size: 2e12, wearout: 97},
		{node: "pve1", dev: "nvme1n1", model: "Samsung SSD 980 PRO 2TB", serial: "S6B0NL0W412377A", typ: "nvme", health: "PASSED", size: 2e12, wearout: 96},
		{node: "pve1", dev: "sda", model: "CT2000MX500SSD1", serial: "2019E2A1B0C4", typ: "ssd", health: "PASSED", size: 2e12, wearout: 8},
		{node: "pve2", dev: "nvme0n1", model: "Samsung SSD 990 PRO 2TB", serial: "S73WNJ0W501122B", typ: "nvme", health: "PASSED", size: 2e12, wearout: 99},
		{node: "pve2", dev: "nvme1n1", model: "Samsung SSD 990 PRO 2TB", serial: "S73WNJ0W501187F", typ: "nvme", health: "PASSED", size: 2e12, wearout: 98},
		{node: "pve3", dev: "sda", model: "Samsung SSD 870 EVO 2TB", serial: "S6PNNM0T812345E", typ: "ssd", health: "PASSED", size: 2e12, wearout: 94},
		{node: "pve3", dev: "sdb", model: "WDC WD40EFRX-68N32N0", serial: "WD-WCC7K1ABCD01", typ: "hdd", health: "PASSED", size: 4e12, wearout: -1},
		{node: "pve3", dev: "sdc", model: "WDC WD40EFRX-68N32N0", serial: "WD-WCC7K1ABCD02", typ: "hdd", health: "PASSED", size: 4e12, wearout: -1},
		{node: "pve3", dev: "sdd", model: "WDC WD40EFRX-68N32N0", serial: "WD-WCC7K1ABCD03", typ: "hdd", health: "PASSED", size: 4e12, wearout: -1},
		{node: "pve3", dev: "sde", model: "WDC WD40EFRX-68N32N0", serial: "WD-WCC7K1ABCD04", typ: "hdd", health: "PASSED", size: 4e12, wearout: -1},
	}

	updates := []aptPkg{
		{Package: "proxmox-ve", Title: "Proxmox Virtual Environment", Version: "8.3.0", OldVersion: "8.2.0", Origin: "Proxmox", Priority: "optional", Section: "admin"},
		{Package: "pve-manager", Title: "Proxmox Virtual Environment Management Tools", Version: "8.3.2", OldVersion: "8.3.0", Origin: "Proxmox", Priority: "optional", Section: "admin"},
		{Package: "proxmox-kernel-6.8", Title: "Latest Proxmox Kernel Image", Version: "6.8.12-5", OldVersion: "6.8.12-4", Origin: "Proxmox", Priority: "optional", Section: "admin"},
		{Package: "libssl3", Title: "Secure Sockets Layer toolkit - shared libraries", Version: "3.0.15-1~deb12u1", OldVersion: "3.0.14-1~deb12u2", Origin: "Debian-Security", Priority: "optional", Section: "libs"},
		{Package: "openssh-server", Title: "secure shell (SSH) server", Version: "1:9.2p1-2+deb12u4", OldVersion: "1:9.2p1-2+deb12u3", Origin: "Debian-Security", Priority: "optional", Section: "net"},
		{Package: "tzdata", Title: "time zone and daylight-saving time data", Version: "2024b-0+deb12u1", OldVersion: "2024a-0+deb12u1", Origin: "Debian", Priority: "required", Section: "localization"},
	}
	s.apt = map[string][]aptPkg{"pve1": updates, "pve2": updates, "pve3": {}}
}

// wave is a sine with the given period, evaluated at the fake time.
func wave(t time.Time, period time.Duration, phase float64) float64 {
	return math.Sin(2*math.Pi*float64(t.UnixMilli())/float64(period.Milliseconds()) + phase)
}

// noise is a small random jitter in [-a, a].
func noise(a float64) float64 { return (rand.Float64()*2 - 1) * a }

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// counter is a monotonic byte counter with average rate r since start; the rate varies
// between 0.5 r and 1.5 r over the period.
func counter(r float64, start, now time.Time, period time.Duration, phase float64) float64 {
	e := now.Sub(start).Seconds()
	if e <= 0 || r <= 0 {
		return 0
	}
	p := period.Seconds()
	return math.Floor(r * (e + 0.5*p/(2*math.Pi)*(math.Sin(2*math.Pi*e/p+phase)-math.Sin(phase))))
}

func (n *node) cpuAt(t time.Time) float64 {
	return clamp(n.cpuBase+0.08*wave(t, 6*time.Hour, n.phase)+0.04*wave(t, 13*time.Minute, n.phase)+noise(0.02), 0.01, 0.99)
}

func (n *node) memAt(t time.Time) float64 {
	return clamp(n.memBase+0.03*wave(t, 24*time.Hour, n.phase)+noise(0.004), 0.05, 0.98)
}

func (g *guest) running() bool { return g.status == "running" }

func (g *guest) cpuAt(t time.Time) float64 {
	if !g.running() {
		return 0
	}
	return clamp(g.cpuBase+g.cpuBase*0.6*wave(t, 3*time.Hour, g.phase)+noise(g.cpuBase*0.2), 0.001, 1)
}

func (g *guest) memAt(t time.Time) float64 {
	if !g.running() {
		return 0
	}
	return math.Floor(g.maxmem * clamp(g.memBase+0.04*wave(t, 8*time.Hour, g.phase)+noise(0.005), 0.02, 0.99))
}

func (s *storage) usedAt(t time.Time) float64 {
	p := s.pct
	if !s.fixed {
		p += 0.004 * wave(t, 24*time.Hour, s.phase)
	}
	return math.Floor(s.total * clamp(p, 0, 1))
}
