package agent

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"

	"pk95monitoring/internal/model"
)

// Check groups (UI sections).
const (
	groupSystem   = "System"
	groupDisks    = "Festplatten"
	groupNet      = "Netzwerk"
	groupServices = "Dienste"
	groupUpdates  = "Updates"
)

// HostInfo describes this host (sent with every report and at registration).
type HostInfo struct {
	Hostname        string
	OS              string
	Platform        string
	PlatformVersion string
	Kernel          string
	Arch            string
	IPs             []string
	Uptime          uint64 // seconds
	Procs           uint64
}

// HostFacts reads the host description.
func HostFacts(ctx context.Context) HostInfo {
	h := HostInfo{OS: runtime.GOOS, Arch: runtime.GOARCH}
	if hi, err := host.InfoWithContext(ctx); err == nil {
		h.Hostname, h.Platform, h.PlatformVersion = hi.Hostname, hi.Platform, hi.PlatformVersion
		h.Kernel, h.Uptime, h.Procs = hi.KernelVersion, hi.Uptime, hi.Procs
	}
	if h.Hostname == "" {
		h.Hostname, _ = os.Hostname()
	}
	h.IPs = localIPs(ctx)
	return h
}

// localIPs lists the global addresses of the monitored interfaces (at most 16).
func localIPs(ctx context.Context) []string {
	ifs, err := gnet.InterfacesWithContext(ctx)
	if err != nil {
		return nil
	}
	var v4, v6 []string
	for _, i := range ifs {
		if skipIface(i.Name) || !slices.Contains(i.Flags, "up") {
			continue
		}
		for _, a := range i.Addrs {
			p, err := netip.ParsePrefix(a.Addr)
			if err != nil {
				continue
			}
			ip := p.Addr()
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
				continue
			}
			if ip.Is4() {
				v4 = append(v4, ip.String())
			} else {
				v6 = append(v6, ip.String())
			}
		}
	}
	all := append(v4, v6...)
	if len(all) > 16 {
		all = all[:16]
	}
	return all
}

// Collector gathers the checks of this host. It keeps the previous CPU and network counters
// to compute usage and rates between two calls, and the platform caches (updates, …).
type Collector struct {
	// WaitSlow makes slow probes (Windows Update search, event log) finish before returning;
	// used by "pk95agent once". The service never waits.
	WaitSlow bool

	mu        sync.Mutex
	primed    bool
	prevCPU   *cpu.TimesStat
	prevNet   map[string]gnet.IOCountersStat
	prevNetAt time.Time
	cpuModel  string
	cores     [2]int // physical, logical
	plat      platformState
}

// NewCollector returns a collector; its first Collect call primes the counters.
func NewCollector() *Collector { return &Collector{} }

var defaultCollector = NewCollector()

// Collect returns the checks of this host using a shared collector (see Collector.Collect).
func Collect(ctx context.Context, cfg Config) []model.Check {
	return defaultCollector.Collect(ctx, cfg)
}

// Collect returns the checks of this host. The first call samples the CPU and network
// counters twice, one second apart, so every report contains usage and rates.
func (c *Collector) Collect(ctx context.Context, cfg Config) []model.Check {
	return c.Report(ctx, cfg).Checks
}

// Report collects all checks and wraps them in a report (Time = now).
func (c *Collector) Report(ctx context.Context, cfg Config) model.AgentReport {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.primed {
		c.primed = true
		c.sampleCPU(ctx)
		c.sampleNet(ctx)
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
		}
	}
	h := HostFacts(ctx)
	checks := []model.Check{c.systemCheck(ctx, h)}
	if ch, ok := c.cpuCheck(ctx); ok {
		checks = append(checks, ch)
	}
	checks = append(checks, memChecks(ctx)...)
	checks = append(checks, diskChecks(ctx)...)
	checks = append(checks, c.netChecks(ctx)...)
	checks = append(checks, c.platformChecks(ctx, cfg, h)...)

	return model.AgentReport{
		AgentID: MachineID(), Hostname: h.Hostname, OS: h.OS, Platform: h.Platform,
		PlatformVersion: h.PlatformVersion, Kernel: h.Kernel, Arch: h.Arch, Version: Version,
		IPs: h.IPs, Uptime: int64(h.Uptime), Time: time.Now().Unix(), Checks: checks,
	}
}

func (c *Collector) systemCheck(ctx context.Context, h HostInfo) model.Check {
	if c.cpuModel == "" {
		if infos, err := cpu.InfoWithContext(ctx); err == nil && len(infos) > 0 {
			c.cpuModel = strings.Join(strings.Fields(infos[0].ModelName), " ")
		}
		c.cores[0], _ = cpu.CountsWithContext(ctx, false)
		c.cores[1], _ = cpu.CountsWithContext(ctx, true)
	}
	osName := strings.TrimSpace(platformName(h.Platform) + " " + h.PlatformVersion)
	facts := map[string]string{
		"Betriebssystem": osName,
		"Kernel":         h.Kernel,
		"Hostname":       h.Hostname,
		"Architektur":    h.Arch,
		"Agent-Version":  Version,
		"Laufzeit":       humanDuration(h.Uptime),
	}
	if c.cpuModel != "" {
		facts["CPU"] = c.cpuModel
	}
	switch {
	case c.cores[0] > 0 && c.cores[1] > c.cores[0]:
		facts["Kerne"] = fmt.Sprintf("%d (%d Threads)", c.cores[0], c.cores[1])
	case c.cores[1] > 0:
		facts["Kerne"] = strconv.Itoa(c.cores[1])
	}
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil && vm.Total > 0 {
		facts["Arbeitsspeicher"] = humanBytes(float64(vm.Total))
	}
	if len(h.IPs) > 0 {
		facts["IP-Adressen"] = strings.Join(h.IPs, ", ")
	}
	for k, v := range facts {
		if v == "" {
			delete(facts, k)
		}
	}
	return model.Check{
		Key: "system", Name: "System", Group: groupSystem, State: model.StateOK,
		Message: strings.TrimSpace(osName + ", Laufzeit " + humanDuration(h.Uptime)),
		Metrics: []model.Metric{
			{Name: "uptime", Value: float64(h.Uptime), Unit: "s"},
			{Name: "procs", Value: float64(h.Procs)},
		},
		Facts: facts,
	}
}

// platformName makes gopsutil's platform IDs readable ("debian" → "Debian").
func platformName(p string) string {
	switch p {
	case "":
		return runtime.GOOS
	case "rhel":
		return "Red Hat Enterprise Linux"
	case "opensuse", "opensuse-leap":
		return "openSUSE"
	case "darwin":
		return "macOS"
	}
	if strings.ContainsAny(p[:1], "abcdefghijklmnopqrstuvwxyz") {
		return strings.ToUpper(p[:1]) + p[1:]
	}
	return p
}

func (c *Collector) sampleCPU(ctx context.Context) (prev, cur *cpu.TimesStat) {
	ts, err := cpu.TimesWithContext(ctx, false)
	if err != nil || len(ts) == 0 {
		return c.prevCPU, nil
	}
	prev, cur = c.prevCPU, &ts[0]
	c.prevCPU = cur
	return prev, cur
}

// cpuUsage returns the busy share in percent between two samples of the CPU times.
func cpuUsage(prev, cur cpu.TimesStat) (float64, bool) {
	total := func(t cpu.TimesStat) float64 {
		// Guest time is already included in user time on Linux.
		return t.User + t.System + t.Idle + t.Nice + t.Iowait + t.Irq + t.Softirq + t.Steal
	}
	idle := func(t cpu.TimesStat) float64 { return t.Idle + t.Iowait }
	dt := total(cur) - total(prev)
	if dt <= 0 {
		return 0, false
	}
	busy := dt - (idle(cur) - idle(prev))
	return min(max(busy/dt*100, 0), 100), true
}

func (c *Collector) cpuCheck(ctx context.Context) (model.Check, bool) {
	prev, cur := c.sampleCPU(ctx)
	if prev == nil || cur == nil {
		return model.Check{}, false
	}
	usage, ok := cpuUsage(*prev, *cur)
	if !ok {
		return model.Check{}, false
	}
	ch := model.Check{
		Key: "cpu", Name: "CPU", Group: groupSystem, State: model.StateOK,
		Message: pct(usage) + " Auslastung",
		Metrics: []model.Metric{{Name: "cpu.usage", Value: round2(usage), Unit: "%"}},
	}
	if runtime.GOOS == "linux" {
		if l, err := load.AvgWithContext(ctx); err == nil {
			ch.Metrics = append(ch.Metrics, model.Metric{Name: "cpu.load1", Value: round2(l.Load1)})
			ch.Message += ", Last " + deNum(l.Load1, 2)
		}
	}
	return ch, true
}

func memChecks(ctx context.Context) []model.Check {
	var out []model.Check
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil && vm.Total > 0 {
		used := vm.Used
		if vm.Available > 0 && vm.Available <= vm.Total {
			used = vm.Total - vm.Available
		}
		usage := float64(used) / float64(vm.Total) * 100
		out = append(out, model.Check{
			Key: "mem", Name: "Arbeitsspeicher", Group: groupSystem, State: model.StateOK,
			Message: fmt.Sprintf("%s von %s belegt (%s)", humanBytes(float64(used)), humanBytes(float64(vm.Total)), pct(usage)),
			Metrics: []model.Metric{
				{Name: "mem.usage", Value: round2(usage), Unit: "%"},
				{Name: "mem.used", Value: float64(used), Unit: "B"},
				{Name: "mem.total", Value: float64(vm.Total), Unit: "B"},
			},
		})
	}
	if sw, err := mem.SwapMemoryWithContext(ctx); err == nil && sw.Total > 0 {
		usage := float64(sw.Used) / float64(sw.Total) * 100
		out = append(out, model.Check{
			Key: "swap", Name: "Auslagerung", Group: groupSystem, State: model.StateOK,
			Message: fmt.Sprintf("%s von %s belegt (%s)", humanBytes(float64(sw.Used)), humanBytes(float64(sw.Total)), pct(usage)),
			Metrics: []model.Metric{{Name: "swap.usage", Value: round2(usage), Unit: "%"}},
		})
	}
	return out
}

func diskChecks(ctx context.Context) []model.Check {
	parts, err := disk.PartitionsWithContext(ctx, false)
	if err != nil {
		return nil
	}
	var out []model.Check
	for _, p := range filterPartitions(parts) {
		u, err := disk.UsageWithContext(ctx, p.Mountpoint)
		if err != nil || u.Total == 0 {
			continue // CD drive without medium, stale network mount …
		}
		mount := p.Mountpoint
		if runtime.GOOS == "windows" {
			mount = winMount(mount)
		}
		facts := map[string]string{"Dateisystem": p.Fstype}
		if p.Device != "" && p.Device != mount {
			facts["Gerät"] = p.Device
		}
		out = append(out, model.Check{
			Key: "disk:" + mount, Name: "Festplatte " + mount, Group: groupDisks, State: model.StateOK,
			Message: fmt.Sprintf("%s belegt, %s von %s frei", pct(u.UsedPercent), humanBytes(float64(u.Free)), humanBytes(float64(u.Total))),
			Metrics: []model.Metric{
				{Name: "disk.usage", Instance: mount, Value: round2(u.UsedPercent), Unit: "%"},
				{Name: "disk.used", Instance: mount, Value: float64(u.Used), Unit: "B"},
				{Name: "disk.total", Instance: mount, Value: float64(u.Total), Unit: "B"},
			},
			Facts: facts,
		})
	}
	return out
}

// winMount normalizes "c:\\" to "C:"; volume mount folders keep their case.
func winMount(m string) string {
	if t := strings.TrimRight(m, `\`); t != "" {
		m = t
	}
	if len(m) == 2 && m[1] == ':' {
		return strings.ToUpper(m)
	}
	return m
}

func (c *Collector) sampleNet(ctx context.Context) (prev map[string]gnet.IOCountersStat, cur []gnet.IOCountersStat, dt float64) {
	stats, err := gnet.IOCountersWithContext(ctx, true)
	if err != nil {
		return nil, nil, 0
	}
	now := time.Now()
	prev, dt = c.prevNet, now.Sub(c.prevNetAt).Seconds()
	c.prevNet = make(map[string]gnet.IOCountersStat, len(stats))
	for _, s := range stats {
		c.prevNet[s.Name] = s
	}
	c.prevNetAt = now
	return prev, stats, dt
}

// rate returns the change per second; a counter reset or wrap yields no value.
func rate(prev, cur uint64, dt float64) (float64, bool) {
	if dt <= 0 || cur < prev {
		return 0, false
	}
	return float64(cur-prev) / dt, true
}

func (c *Collector) netChecks(ctx context.Context) []model.Check {
	prev, cur, dt := c.sampleNet(ctx)
	var out []model.Check
	for _, s := range cur {
		if skipIface(s.Name) || s.BytesRecv+s.BytesSent == 0 {
			continue
		}
		p, ok := prev[s.Name]
		if !ok {
			continue
		}
		rx, ok1 := rate(p.BytesRecv, s.BytesRecv, dt)
		tx, ok2 := rate(p.BytesSent, s.BytesSent, dt)
		if !ok1 || !ok2 {
			continue
		}
		out = append(out, model.Check{
			Key: "if:" + s.Name, Name: "Netzwerk " + s.Name, Group: groupNet, State: model.StateOK,
			Message: fmt.Sprintf("Empfang %s, Senden %s", humanRate(rx), humanRate(tx)),
			Metrics: []model.Metric{
				{Name: "net.rx", Instance: s.Name, Value: round2(rx), Unit: "B/s"},
				{Name: "net.tx", Instance: s.Name, Value: round2(tx), Unit: "B/s"},
			},
		})
	}
	slices.SortFunc(out, func(a, b model.Check) int { return strings.Compare(a.Key, b.Key) })
	return out
}
