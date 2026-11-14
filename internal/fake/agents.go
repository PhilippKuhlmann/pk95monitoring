package fake

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"pk95monitoring/internal/core"
	"pk95monitoring/internal/hyperv"
	"pk95monitoring/internal/model"
)

// Check groups of the agent (same as internal/agent).
const (
	agSystem   = "System"
	agDisks    = "Festplatten"
	agNet      = "Netzwerk"
	agServices = "Dienste"
	agUpdates  = "Updates"
)

const gib = 1 << 30

type simDisk struct {
	mount, fs, dev string
	total          float64 // bytes
	pct, amp       float64 // usage in % and its daily swing
}

type simService struct {
	display string
	running bool
}

// simAgent simulates one pk95agent host; all values are functions of time.
type simAgent struct {
	id, name, group, ip string
	os, platform, ver   string // gopsutil platform and version
	kernel, cpuModel    string
	cores, threads      int
	memGB               float64
	cpuBase, cpuAmp     float64
	memBase, memAmp     float64
	swapPct             float64
	disks               []simDisk
	iface               string
	rxBase, txBase      float64 // bytes/s
	services            []string
	known               map[string]simService
	pending, security   int
	installedDays       int      // windows: days since the last update installation
	failed              int      // windows: failed updates (30 days)
	titles              []string // windows: pending update titles
	boot                int64
	seed                float64
	rules               []model.CheckRule // demo check rules (quiet / off)
	parent, node        string            // attached to this demo device (and its node)
	hyperV              []simVM           // windows: VMs of a Hyper-V host
}

// simVM is a demo Hyper-V VM; checkpoint and replication times are relative to t.
type simVM struct {
	id, name, state, autoStart string
	procs                      int
	memGB, cpuBase, demandPct  float64
	bootAgo                    int64 // s
	checkpoints                int
	checkpointAgo              int64  // s, oldest checkpoint
	replHealth, replMode       string // "" = not replicated
}

func demoAgents(now time.Time) []*simAgent {
	day := int64(86400)
	t := now.Unix()
	return []*simAgent{
		{id: "demo-dc01", name: "dc01", group: groupServer, ip: "10.0.0.11", os: "windows",
			platform: "Microsoft Windows Server 2022 Standard", ver: "10.0.20348 Build 20348", kernel: "10.0.20348 Build 20348",
			cpuModel: "Intel(R) Xeon(R) Silver 4314 CPU @ 2.40GHz", cores: 4, threads: 4, memGB: 16,
			cpuBase: 12, cpuAmp: 8, memBase: 58, memAmp: 6,
			disks: []simDisk{{mount: "C:", fs: "NTFS", total: 120 * gib, pct: 46, amp: 0.4}},
			iface: "Ethernet0", rxBase: 180e3, txBase: 95e3,
			known: map[string]simService{"NTDS": {"Active Directory-Domänendienste", true}, "DNS": {"DNS-Server", true},
				"W32Time": {"Windows-Zeitgeber", true}, "Spooler": {"Druckwarteschlange", true},
				"VeeamEndpointBackupSvc": {"Veeam Agent for Microsoft Windows", true}},
			pending: 2, boot: t - 23*day - 4*3600, seed: 1.3, installedDays: 9,
			titles: []string{"Security Intelligence-Update für Microsoft Defender Antivirus – KB2267602", "Microsoft .NET Framework 4.8.1 für Windows Server 2022 (KB5046540)"}},
		{id: "demo-fs01", name: "fs01", group: groupServer, ip: "10.0.0.12", os: "windows",
			platform: "Microsoft Windows Server 2019 Standard", ver: "10.0.17763 Build 17763", kernel: "10.0.17763 Build 17763",
			cpuModel: "Intel(R) Xeon(R) Silver 4314 CPU @ 2.40GHz", cores: 4, threads: 4, memGB: 8,
			cpuBase: 9, cpuAmp: 6, memBase: 64, memAmp: 5,
			disks: []simDisk{{mount: "C:", fs: "NTFS", total: 80 * gib, pct: 61, amp: 0.3},
				{mount: "E:", fs: "NTFS", total: 2048 * gib, pct: 72, amp: 1.5}},
			iface: "Ethernet0", rxBase: 1.2e6, txBase: 2.8e6, services: []string{"Spooler"},
			known: map[string]simService{"Spooler": {"Druckwarteschlange", false}, "LanmanServer": {"Server", true},
				"W32Time": {"Windows-Zeitgeber", true}, "VeeamBackupSvc": {"Veeam Backup Service", true},
				"VeeamTransportSvc": {"Veeam Data Mover Service", true}, "VeeamDeploySvc": {"Veeam Installer Service", false}},
			pending: 1, security: 1, boot: t - 41*day - 9*3600, seed: 2.7, installedDays: 47, failed: 1,
			titles: []string{"2026-11 Kumulatives Update für Windows Server 2019 für x64-basierte Systeme (KB5046615)"}},
		{id: "demo-web01", name: "web01", group: groupWeb, ip: "10.0.0.21", os: "linux",
			platform: "debian", ver: "13.1", kernel: "6.12.48+deb13-amd64",
			cpuModel: "AMD EPYC 7313P 16-Core Processor", cores: 2, threads: 2, memGB: 4,
			cpuBase: 22, cpuAmp: 15, memBase: 48, memAmp: 10, swapPct: 3,
			disks: []simDisk{{mount: "/", fs: "ext4", dev: "/dev/sda1", total: 40 * gib, pct: 90.8, amp: 0.6},
				{mount: "/boot", fs: "ext4", dev: "/dev/sda2", total: 0.5 * gib, pct: 21, amp: 0}},
			iface: "ens18", rxBase: 420e3, txBase: 1.9e6, services: []string{"nginx"},
			known: map[string]simService{"nginx": {"nginx", true}, "ssh": {"ssh", true}, "cron": {"cron", true}},
			boot:  t - 12*day - 2*3600, seed: 3.1,
			rules: []model.CheckRule{{Match: "disk:/boot*", Mode: model.ModeOff}}},
		{id: "demo-db01", name: "db01", group: groupServer, ip: "10.0.0.22", os: "linux",
			platform: "debian", ver: "12.12", kernel: "6.1.0-40-amd64",
			cpuModel: "AMD EPYC 7313P 16-Core Processor", cores: 4, threads: 8, memGB: 16,
			cpuBase: 18, cpuAmp: 12, memBase: 71, memAmp: 4, swapPct: 9,
			disks: []simDisk{{mount: "/", fs: "ext4", dev: "/dev/sda1", total: 32 * gib, pct: 38, amp: 0.3},
				{mount: "/var/lib/postgresql", fs: "xfs", dev: "/dev/sdb1", total: 500 * gib, pct: 57, amp: 2}},
			iface: "ens18", rxBase: 650e3, txBase: 380e3, services: []string{"postgresql"},
			known:   map[string]simService{"postgresql": {"postgresql", true}, "ssh": {"ssh", true}},
			pending: 7, security: 3, boot: t - 64*day - 5*3600, seed: 4.6,
			rules: []model.CheckRule{{Match: "updates", Mode: model.ModeQuiet}}},
		// Agent on a node of the demo Proxmox cluster: it complements the API data with the
		// host's file systems, services and apt updates. The updates are quiet because the
		// cluster check "Updates pve1" alerts already.
		{id: "demo-pve1", name: "pve1", group: groupServer, ip: "10.0.10.11", os: "linux",
			platform: "debian", ver: "13.1", kernel: "6.14.11-4-pve",
			cpuModel: "AMD EPYC 4344P 8-Core Processor", cores: 8, threads: 16, memGB: 128,
			cpuBase: 19, cpuAmp: 9, memBase: 62, memAmp: 3, swapPct: 1,
			disks: []simDisk{{mount: "/", fs: "ext4", dev: "/dev/mapper/pve-root", total: 94 * gib, pct: 21, amp: 0.2},
				{mount: "/var/lib/vz", fs: "ext4", dev: "/dev/mapper/pve-data", total: 1700 * gib, pct: 48, amp: 0.8}},
			iface: "vmbr0", rxBase: 3.5e6, txBase: 2.2e6, services: []string{"pveproxy", "pvedaemon", "corosync"},
			known: map[string]simService{"pveproxy": {"pveproxy", true}, "pvedaemon": {"pvedaemon", true},
				"corosync": {"corosync", true}, "pve-cluster": {"pve-cluster", true}, "ssh": {"ssh", true}},
			pending: 6, security: 2, boot: t - 41*day, seed: 5.2,
			rules:  []model.CheckRule{{Match: "updates", Mode: model.ModeQuiet}},
			parent: "demo-pve", node: "pve1"},
		// Hyper-V host: a replicated domain controller, a VM with a forgotten checkpoint, a
		// replication with warnings, a VM that should run but is off and a switched-off test VM.
		{id: "demo-hv01", name: "hv01", group: groupServer, ip: "10.0.0.15", os: "windows",
			platform: "Microsoft Windows Server 2022 Datacenter", ver: "10.0.20348 Build 20348", kernel: "10.0.20348 Build 20348",
			cpuModel: "Intel(R) Xeon(R) Gold 5418Y", cores: 24, threads: 48, memGB: 256,
			cpuBase: 21, cpuAmp: 9, memBase: 71, memAmp: 2,
			disks: []simDisk{{mount: "C:", fs: "NTFS", total: 240 * gib, pct: 38, amp: 0.2},
				{mount: "D:", fs: "ReFS", total: 7450 * gib, pct: 64, amp: 0.4}},
			iface: "vEthernet (LAN)", rxBase: 9.5e6, txBase: 7.2e6, services: []string{"vmms"},
			known: map[string]simService{"vmms": {"Hyper-V-VM-Verwaltung", true}, "W32Time": {"Windows-Zeitgeber", true},
				"vmcompute": {"Hyper-V-Hostcomputedienst", true}},
			boot: t - 33*day - 2*3600, seed: 6.4, installedDays: 21,
			hyperV: []simVM{
				{id: "6f1c2a8e-0001-4c55-9a1e-3b7d2f0a1101", name: "dc02", state: "Running", autoStart: "StartIfRunning", procs: 4, memGB: 8, cpuBase: 6, demandPct: 52,
					bootAgo: 33*day - 1800, replMode: "Primary", replHealth: "Normal"},
				{id: "6f1c2a8e-0002-4c55-9a1e-3b7d2f0a1102", name: "erp01", state: "Running", autoStart: "StartIfRunning", procs: 8, memGB: 32, cpuBase: 34, demandPct: 78,
					bootAgo: 12*day + 3600, checkpoints: 1, checkpointAgo: 12*day + 5400},
				{id: "6f1c2a8e-0003-4c55-9a1e-3b7d2f0a1103", name: "sql02", state: "Running", autoStart: "StartIfRunning", procs: 8, memGB: 48, cpuBase: 22, demandPct: 88,
					bootAgo: 33*day - 1500, replMode: "Primary", replHealth: "Warning"},
				{id: "6f1c2a8e-0004-4c55-9a1e-3b7d2f0a1104", name: "print01", state: "Off", autoStart: "Start", procs: 2, memGB: 4},
				{id: "6f1c2a8e-0005-4c55-9a1e-3b7d2f0a1105", name: "test-win11", state: "Off", autoStart: "Nothing", procs: 4, memGB: 8},
			}},
	}
}

// hyperVReport is the Hyper-V state of a demo host at t (what hyperv.Script would print).
func (a *simAgent) hyperVReport(t int64) hyperv.Report {
	r := hyperv.Report{Available: true, Host: strings.ToUpper(a.name)}
	for i, v := range a.hyperV {
		vm := hyperv.VM{ID: v.id, Name: v.name, State: v.state, AutoStart: v.autoStart, Procs: v.procs,
			Startup: v.memGB * gib, Generation: 2, Version: "10.0", Checkpoints: v.checkpoints}
		if v.state == "Running" {
			vm.Heartbeat, vm.Status = "OkApplicationsHealthy", "Wird normal ausgeführt"
			vm.CPU = math.Round(clamp(v.cpuBase*(1+0.6*a.wave(t, 10+float64(i))), 0, 100))
			vm.Mem = v.memGB * gib
			vm.Demand = math.Round(vm.Mem * clamp(v.demandPct+4*a.wave(t, 20+float64(i)), 5, 100) / 100)
			vm.Uptime = v.bootAgo
		}
		if v.checkpoints > 0 {
			vm.OldestCheckpoint = t - v.checkpointAgo
		}
		if v.replMode != "" {
			last := t - 120 - (t % 300) // every 5 minutes
			if v.replHealth == "Warning" {
				last = t - 47*60 - (t % 300) // several cycles missed
			}
			vm.Replication = &hyperv.Replication{State: "Replicating", Health: v.replHealth, Mode: v.replMode,
				Primary: strings.ToUpper(a.name), Replica: "HV02", Last: last}
		}
		r.VMs = append(r.VMs, vm)
	}
	return r
}

// selectServices mirrors the agent's selection: listed names, plus the known services a
// pattern of the device's list or an entry of the central list matches (by name or display
// name) that run (the simulation treats running as "starts automatically"), minus ignored ones.
func (a *simAgent) selectServices(listed, central, ignore []string) []string {
	match := func(p, n string) bool {
		return model.GlobMatch(strings.ToLower(strings.TrimSpace(p)), strings.ToLower(n))
	}
	var out, patterns []string
	seen := map[string]bool{}
	for _, s := range listed {
		switch s = strings.TrimSpace(s); {
		case s == "":
		case strings.Contains(s, "*"):
			patterns = append(patterns, s)
		case !seen[strings.ToLower(s)]:
			seen[strings.ToLower(s)] = true
			out = append(out, s)
		}
	}
	patterns = append(patterns, central...)
	names := slices.Sorted(maps.Keys(a.known))
	for _, n := range names {
		sv := a.known[n]
		hit := func(p string) bool { return strings.TrimSpace(p) != "" && (match(p, n) || match(p, sv.display)) }
		if seen[strings.ToLower(n)] || !sv.running || !slices.ContainsFunc(patterns, hit) || slices.ContainsFunc(ignore, hit) {
			continue
		}
		seen[strings.ToLower(n)] = true
		out = append(out, n)
	}
	return out
}

// device is the demo device of the agent (as registration would create it).
func (a *simAgent) device(ts int64) model.Device {
	return model.Device{ID: a.id, Name: a.name, Type: model.TypeAgent, Host: a.ip, Group: a.group, Enabled: true,
		ParentID: a.parent, Node: a.node,
		Checks: []model.NetCheck{}, Thresholds: []model.Threshold{}, Rules: slices.Clone(a.rules), Created: ts, Updated: ts,
		Agent: &model.AgentConfig{AgentID: a.id + "-machine", OS: a.os, Platform: a.platform, PlatformVersion: a.ver,
			Kernel: a.kernel, Arch: "amd64", Hostname: a.name, IPs: []string{a.ip},
			Services: slices.Clone(a.services), IgnoreServices: []string{}}}
}

// report is the agent report at time t; services follow the device configuration.
func (a *simAgent) report(c *core.Core, t time.Time) model.AgentReport {
	services, ignore := a.services, []string(nil)
	if d, err := c.Device(a.id); err == nil && d.Agent != nil {
		services, ignore = d.Agent.Services, d.Agent.IgnoreServices
	}
	central := c.Settings().Agent.LinuxServices
	if a.os == "windows" {
		central = c.Settings().Agent.WindowsServices
	}
	services = a.selectServices(services, central, ignore)
	return model.AgentReport{AgentID: a.id + "-machine", Hostname: a.name, OS: a.os, Platform: a.platform,
		PlatformVersion: a.ver, Kernel: a.kernel, Arch: "amd64", Version: agentVersion(c), IPs: []string{a.ip},
		Uptime: t.Unix() - a.boot, Time: t.Unix(), Checks: a.checks(c, t.Unix(), services)}
}

func agentVersion(c *core.Core) string {
	if v := c.Agents.Version(); v != "" {
		return v
	}
	return "dev"
}

// wave is a daily sine (busy in the afternoon) plus a faster ripple and noise in [-1, 1].
func (a *simAgent) wave(t int64, k float64) float64 {
	day := 2 * math.Pi * float64(t%86400) / 86400
	return 0.6*math.Sin(day-2+a.seed*k) + 0.25*math.Sin(float64(t)/1700+a.seed*k*3) + 0.3*(noise(a.seed*k, t)-0.5)
}

// noise returns a reproducible pseudo-random number in [0, 1) for a seed and time.
func noise(seed float64, t int64) float64 {
	v := math.Sin(seed*12.9898+float64(t/60)*78.233) * 43758.5453
	return v - math.Floor(v)
}

func clamp(v, lo, hi float64) float64 { return math.Min(math.Max(v, lo), hi) }

// checks builds the checks exactly like internal/agent does on a real host.
func (a *simAgent) checks(c *core.Core, t int64, services []string) []model.Check {
	uptime := uint64(max(t-a.boot, 0))
	cpu := round2(clamp(a.cpuBase+a.cpuAmp*a.wave(t, 1), 0.5, 100))
	osName := strings.TrimSpace(platformName(a.platform) + " " + a.ver)
	facts := map[string]string{
		"Betriebssystem": osName, "Kernel": a.kernel, "Hostname": a.name, "Architektur": "amd64",
		"Agent-Version": agentVersion(c), "Laufzeit": humanDuration(uptime), "CPU": a.cpuModel,
		"Arbeitsspeicher": humanBytes(a.memGB * gib), "IP-Adressen": a.ip,
	}
	if a.threads > a.cores {
		facts["Kerne"] = fmt.Sprintf("%d (%d Threads)", a.cores, a.threads)
	} else {
		facts["Kerne"] = strconv.Itoa(a.threads)
	}
	procs := 120 + 30*a.wave(t, 5)
	if a.os == "windows" {
		procs += 40
	}
	out := []model.Check{{
		Key: "system", Name: "System", Group: agSystem, State: model.StateOK,
		Message: strings.TrimSpace(osName + ", Laufzeit " + humanDuration(uptime)),
		Metrics: []model.Metric{{Name: "uptime", Value: float64(uptime), Unit: "s"}, {Name: "procs", Value: math.Round(procs)}},
		Facts:   facts,
	}}

	cpuCheck := model.Check{Key: "cpu", Name: "CPU", Group: agSystem, State: model.StateOK, Message: pct(cpu) + " Auslastung",
		Metrics: []model.Metric{{Name: "cpu.usage", Value: cpu, Unit: "%"}}}
	if a.os == "linux" {
		load := round2(math.Max(cpu/100*float64(a.threads)*1.1, 0.01))
		cpuCheck.Metrics = append(cpuCheck.Metrics, model.Metric{Name: "cpu.load1", Value: load})
		cpuCheck.Message += ", Last " + deNum(load, 2)
	}
	out = append(out, cpuCheck)

	memPct := round2(clamp(a.memBase+a.memAmp*a.wave(t, 2), 1, 99))
	memTotal := a.memGB * gib
	memUsed := math.Round(memTotal * memPct / 100)
	out = append(out, model.Check{Key: "mem", Name: "Arbeitsspeicher", Group: agSystem, State: model.StateOK,
		Message: fmt.Sprintf("%s von %s belegt (%s)", humanBytes(memUsed), humanBytes(memTotal), pct(memPct)),
		Metrics: []model.Metric{{Name: "mem.usage", Value: memPct, Unit: "%"}, {Name: "mem.used", Value: memUsed, Unit: "B"},
			{Name: "mem.total", Value: memTotal, Unit: "B"}}})
	if a.swapPct > 0 {
		swTotal := 2.0 * gib
		sw := round2(clamp(a.swapPct+a.swapPct/3*a.wave(t, 3), 0, 100))
		out = append(out, model.Check{Key: "swap", Name: "Auslagerung", Group: agSystem, State: model.StateOK,
			Message: fmt.Sprintf("%s von %s belegt (%s)", humanBytes(swTotal*sw/100), humanBytes(swTotal), pct(sw)),
			Metrics: []model.Metric{{Name: "swap.usage", Value: sw, Unit: "%"}}})
	}

	for i, d := range a.disks {
		p := round2(clamp(d.pct+d.amp*a.wave(t, 4+float64(i)), 0, 100))
		used := math.Round(d.total * p / 100)
		f := map[string]string{"Dateisystem": d.fs}
		if d.dev != "" {
			f["Gerät"] = d.dev
		}
		out = append(out, model.Check{Key: "disk:" + d.mount, Name: "Festplatte " + d.mount, Group: agDisks, State: model.StateOK,
			Message: fmt.Sprintf("%s belegt, %s von %s frei", pct(p), humanBytes(d.total-used), humanBytes(d.total)),
			Metrics: []model.Metric{
				{Name: "disk.usage", Instance: d.mount, Value: p, Unit: "%"},
				{Name: "disk.used", Instance: d.mount, Value: used, Unit: "B"},
				{Name: "disk.total", Instance: d.mount, Value: d.total, Unit: "B"},
			}, Facts: f})
	}

	rx := round2(math.Max(a.rxBase*(1+0.7*a.wave(t, 6)), 0))
	tx := round2(math.Max(a.txBase*(1+0.7*a.wave(t, 7)), 0))
	out = append(out, model.Check{Key: "if:" + a.iface, Name: "Netzwerk " + a.iface, Group: agNet, State: model.StateOK,
		Message: fmt.Sprintf("Empfang %s, Senden %s", humanBytes(rx)+"/s", humanBytes(tx)+"/s"),
		Metrics: []model.Metric{{Name: "net.rx", Instance: a.iface, Value: rx, Unit: "B/s"},
			{Name: "net.tx", Instance: a.iface, Value: tx, Unit: "B/s"}}})

	if a.os == "linux" {
		out = append(out, model.Check{Key: "systemd", Name: "Systemd-Units", Group: agServices, State: model.StateOK,
			Message: "Keine fehlgeschlagenen Units", Metrics: []model.Metric{{Name: "services.failed", Value: 0}}})
	}
	for _, name := range services {
		s, ok := a.known[name]
		ch := model.Check{Key: "svc:" + name, Name: "Dienst " + name, Group: agServices}
		switch {
		case !ok && a.os == "windows":
			ch.State, ch.Message = model.StateCrit, "Dienst nicht gefunden"
		case !ok:
			ch.State, ch.Message = model.StateCrit, "nicht gefunden"
		case a.os == "windows":
			ch.Name, ch.Facts = "Dienst "+s.display, map[string]string{"Dienstname": name}
			ch.State, ch.Message = model.StateOK, "läuft"
			if !s.running {
				ch.State, ch.Message = model.StateCrit, "beendet"
			}
		default:
			ch.State, ch.Message = model.StateOK, "läuft"
			if !s.running {
				ch.State, ch.Message = model.StateCrit, "gestoppt"
			}
		}
		out = append(out, ch)
	}

	up := model.Check{Key: "updates", Name: "Updates", Group: agUpdates, State: model.StateOK,
		Message: updatesMessage(a.pending, a.security),
		Metrics: []model.Metric{{Name: "updates.pending", Value: float64(a.pending)},
			{Name: "updates.security", Value: float64(a.security)}}}
	if a.os == "windows" {
		last := t - int64(a.installedDays)*86400 - 3*3600
		up.Metrics = append(up.Metrics, model.Metric{Name: "updates.age", Value: float64(a.installedDays), Unit: "d"},
			model.Metric{Name: "updates.failed", Value: float64(a.failed)})
		up.Facts = map[string]string{"Letzte Installation": time.Unix(last, 0).Format("02.01.2006 15:04")}
		up.Message += fmt.Sprintf(" · letzte Installation vor %d Tagen", a.installedDays)
		if a.failed > 0 {
			up.Facts["Fehlgeschlagen (30 Tage)"] = strconv.Itoa(a.failed)
			up.Message += fmt.Sprintf(" · %d fehlgeschlagen", a.failed)
		}
		if len(a.titles) > 0 {
			up.Facts["Ausstehend"] = strings.Join(a.titles, " · ")
		}
	}
	out = append(out, up)
	if a.os == "windows" || a.os == "linux" {
		out = append(out, a.eventChecks(c, t)...)
	}
	if len(a.hyperV) > 0 {
		out = append(out, hyperv.Checks(a.hyperVReport(t), nil, time.Unix(t, 0))...)
	}
	return out
}

// ---- formatting, as in internal/agent ----

func updatesMessage(pending, security int) string {
	switch {
	case pending == 0:
		return "System ist aktuell"
	case pending == 1 && security == 1:
		return "1 Update (Sicherheitsupdate)"
	case pending == 1:
		return "1 Update verfügbar"
	case security == 0:
		return fmt.Sprintf("%d Updates verfügbar", pending)
	case security == 1:
		return fmt.Sprintf("%d Updates, davon 1 Sicherheitsupdate", pending)
	}
	return fmt.Sprintf("%d Updates, davon %d Sicherheitsupdates", pending, security)
}

func platformName(p string) string {
	if p != "" && p[0] >= 'a' && p[0] <= 'z' {
		return strings.ToUpper(p[:1]) + p[1:]
	}
	return p
}

func deNum(v float64, decimals int) string {
	return strings.Replace(fmt.Sprintf("%.*f", decimals, v), ".", ",", 1)
}

func humanBytes(b float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	for math.Abs(b) >= 1024 && i < len(units)-1 {
		b /= 1024
		i++
	}
	if i == 0 || b >= 100 {
		return deNum(b, 0) + " " + units[i]
	}
	return deNum(b, 1) + " " + units[i]
}

func humanDuration(sec uint64) string {
	d, h, m := sec/86400, sec%86400/3600, sec%3600/60
	switch {
	case d > 0:
		day := "Tage"
		if d == 1 {
			day = "Tag"
		}
		return fmt.Sprintf("%d %s %d Std.", d, day, h)
	case h > 0:
		return fmt.Sprintf("%d Std. %d Min.", h, m)
	}
	return fmt.Sprintf("%d Min.", m)
}

func pct(v float64) string {
	if v < 10 && v != 0 {
		return deNum(v, 1) + " %"
	}
	return deNum(v, 0) + " %"
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
