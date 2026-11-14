package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	"pk95monitoring/internal/model"
	"pk95monitoring/internal/netscan"
)

// Network scan: searches the customer's networks for devices that are not monitored yet and
// suggests how to monitor them (netscan). One scan runs at a time in the background; the
// last result is kept in the settings (keyScan) so the page shows it after a restart.
// The monitoring usually runs in a container whose own network is not the customer's, so
// the default ranges are the /24 networks of the monitored devices and agents.

const (
	keyScan        = "netscan"        // settings key: scanResult
	keyScanIgnore  = "netscan_ignore" // settings key: []string of ignored addresses
	scanMaxRuntime = 30 * time.Minute
	maxScanRanges  = 16
)

// ErrScanRunning is returned when a scan is started while one runs.
var ErrScanRunning = errors.New("Es läuft bereits ein Netzwerk-Scan.")

// scanResult is the stored result of the last scan.
type scanResult struct {
	Started   int64          `json:"started"`
	Finished  int64          `json:"finished"`
	Ranges    []string       `json:"ranges"`
	Community string         `json:"community"`
	Hosts     []netscan.Host `json:"hosts"`
}

// scanRun is the state of a running scan.
type scanRun struct {
	running     bool
	started     int64
	ranges      []string
	done, total int
}

// ScanHost is a found host with what the monitoring knows about it.
type ScanHost struct {
	netscan.Host
	DeviceID   string `json:"device_id,omitempty"` // already monitored by this device
	DeviceName string `json:"device_name,omitempty"`
	Ignored    bool   `json:"ignored,omitempty"`
}

// ScanState is the network scan page.
type ScanState struct {
	Running   bool       `json:"running"`
	Started   int64      `json:"started"`
	Finished  int64      `json:"finished"`
	Ranges    []string   `json:"ranges"`
	Community string     `json:"community"`
	Done      int        `json:"done"`
	Total     int        `json:"total"`
	Hosts     []ScanHost `json:"hosts"`
	Defaults  []string   `json:"defaults"` // suggested ranges: the networks of the monitored devices
}

// ScanState returns the running or last scan with the hosts matched to the devices.
func (c *Core) ScanState() ScanState {
	c.scanMu.Lock()
	run, last, ignore := c.scanRun, c.scanLast, slices.Clone(c.scanIgnore)
	c.scanMu.Unlock()
	st := ScanState{Running: run.running, Started: last.Started, Finished: last.Finished, Ranges: last.Ranges,
		Community: last.Community, Hosts: []ScanHost{}, Defaults: c.defaultScanRanges()}
	if st.Community == "" {
		st.Community = "public"
	}
	if run.running {
		st.Started, st.Ranges, st.Done, st.Total = run.started, run.ranges, run.done, run.total
	}
	if st.Ranges == nil {
		st.Ranges = []string{}
	}
	known := c.knownAddrs()
	for _, h := range last.Hosts {
		sh := ScanHost{Host: h, Ignored: slices.Contains(ignore, h.IP)}
		if d, ok := known[h.IP]; ok {
			sh.DeviceID, sh.DeviceName = d.ID, d.Name
		}
		st.Hosts = append(st.Hosts, sh)
	}
	return st
}

type knownDevice struct{ ID, Name string }

// knownAddrs maps the IPv4 addresses of the monitored devices (host, agent addresses, ping,
// TCP and TLS targets) to the device; attached agents count for their parent.
func (c *Core) knownAddrs() map[string]knownDevice {
	c.scanMu.Lock()
	names := c.scanNames
	c.scanMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]knownDevice{}
	for _, d := range c.devices {
		owner := d
		if p, ok := c.parentOf(d); ok {
			owner = p
		}
		k := knownDevice{ID: owner.ID, Name: owner.Name}
		add := func(s string) {
			s, _, _ = strings.Cut(strings.TrimSpace(s), "/")
			ip := ""
			if a, err := netip.ParseAddr(s); err == nil && a.Is4() {
				ip = a.String()
			} else {
				ip = names[strings.ToLower(s)]
			}
			if _, ok := out[ip]; ip != "" && !ok {
				out[ip] = k
			}
		}
		add(d.Host)
		if d.Agent != nil {
			for _, ip := range d.Agent.IPs {
				add(ip)
			}
		}
		for _, n := range d.Checks {
			if n.Type == model.NetPing || n.Type == model.NetTCP || n.Type == model.NetTLS {
				h, _, err := net.SplitHostPort(n.Target)
				if err != nil {
					h = n.Target
				}
				add(h)
			}
		}
	}
	return out
}

// scanAddr reports whether a device address is worth scanning around: private IPv4, not
// loopback, link-local or the Docker default bridge.
func scanAddr(a netip.Addr) bool {
	docker := netip.MustParsePrefix("172.17.0.0/16")
	return a.Is4() && a.IsPrivate() && !a.IsLoopback() && !a.IsLinkLocalUnicast() && !docker.Contains(a)
}

// defaultScanRanges are the /24 networks of the devices' and agents' addresses (the 8 with
// the most devices).
func (c *Core) defaultScanRanges() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := map[string]int{}
	add := func(s string) {
		s, _, _ = strings.Cut(strings.TrimSpace(s), "/")
		a, err := netip.ParseAddr(s)
		if err != nil || !scanAddr(a) {
			return
		}
		p, _ := a.Prefix(24)
		count[p.String()]++
	}
	for _, d := range c.devices {
		add(d.Host)
		if d.Agent != nil {
			for _, ip := range d.Agent.IPs {
				add(ip)
			}
		}
	}
	out := make([]string, 0, len(count))
	for p := range count {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b string) int {
		if count[a] != count[b] {
			return count[b] - count[a]
		}
		return strings.Compare(a, b)
	})
	if len(out) > 8 {
		out = out[:8]
	}
	slices.Sort(out)
	return out
}

// StartScan starts a scan of ranges in the background. Field errors are keyed "ranges" and
// "community"; ErrScanRunning when one runs.
func (c *Core) StartScan(ranges []string, community string) (map[string]string, error) {
	community = strings.TrimSpace(community)
	if len(ranges) > maxScanRanges {
		return map[string]string{"ranges": fmt.Sprintf("Höchstens %d Bereiche.", maxScanRanges)}, nil
	}
	addrs, norm, err := netscan.Expand(ranges)
	switch {
	case err != nil:
		return map[string]string{"ranges": err.Error() + "."}, nil
	case len(addrs) == 0:
		return map[string]string{"ranges": "Mindestens ein Netz oder eine Adresse angeben, z. B. 192.168.1.0/24."}, nil
	case len(community) > 64 || strings.ContainsAny(community, " \t\r\n"):
		return map[string]string{"community": "Ungültige Community."}, nil
	}
	c.scanMu.Lock()
	if c.scanRun.running {
		c.scanMu.Unlock()
		return nil, ErrScanRunning
	}
	now := c.Now().Unix()
	ctx, cancel := context.WithTimeout(context.Background(), scanMaxRuntime)
	c.scanRun = scanRun{running: true, started: now, ranges: norm, total: len(addrs)}
	c.scanCancel = cancel
	c.scanMu.Unlock()
	c.Event("info", "Netzwerk-Scan", "Netzwerk-Scan gestartet", fmt.Sprintf("%s (%d Adressen).", strings.Join(norm, ", "), len(addrs)), nil, false)

	c.running.Add(1)
	go func() {
		defer c.running.Done()
		defer cancel()
		names := c.resolveDeviceNames(ctx)
		hosts := c.Scanner(ctx, netscan.Options{Addrs: addrs, Community: community, Progress: func(done, total int) {
			c.scanMu.Lock()
			c.scanRun.done, c.scanRun.total = done, total
			c.scanMu.Unlock()
		}})
		if hosts == nil {
			hosts = []netscan.Host{}
		}
		res := scanResult{Started: now, Finished: c.Now().Unix(), Ranges: norm, Community: community, Hosts: hosts}
		c.scanMu.Lock()
		c.scanLast, c.scanRun, c.scanNames, c.scanCancel = res, scanRun{}, names, nil
		c.scanMu.Unlock()
		if ctx.Err() == context.Canceled {
			return // shutdown: the partial result is not stored
		}
		if err := c.St.SetSetting(keyScan, res); err != nil {
			c.Event("warning", "Netzwerk-Scan", "Ergebnis nicht gespeichert", err.Error(), nil, false)
		}
		known := c.knownAddrs()
		fresh := 0
		for _, h := range hosts {
			if _, ok := known[h.IP]; !ok && h.Suggest != "" {
				fresh++
			}
		}
		c.Event("info", "Netzwerk-Scan", "Netzwerk-Scan beendet", fmt.Sprintf("%d %s gefunden, %d davon noch nicht überwacht.",
			len(hosts), plural1(len(hosts), "Gerät", "Geräte"), fresh), nil, false)
	}()
	return nil, nil
}

// stopScan cancels a running scan (shutdown).
func (c *Core) stopScan() {
	c.scanMu.Lock()
	defer c.scanMu.Unlock()
	if c.scanCancel != nil {
		c.scanCancel()
	}
}

// resolveDeviceNames resolves the host names of the devices, so devices added by name are
// recognized in the scan.
func (c *Core) resolveDeviceNames(ctx context.Context) map[string]string {
	c.mu.Lock()
	var names []string
	for _, d := range c.devices {
		if h := strings.TrimSpace(d.Host); h != "" {
			if _, err := netip.ParseAddr(h); err != nil {
				names = append(names, strings.ToLower(h))
			}
		}
	}
	c.mu.Unlock()
	out := map[string]string{}
	for _, n := range names {
		rc, cancel := context.WithTimeout(ctx, 2*time.Second)
		ips, err := net.DefaultResolver.LookupNetIP(rc, "ip4", n)
		cancel()
		if err == nil && len(ips) > 0 {
			out[n] = ips[0].Unmap().String()
		}
	}
	return out
}

// SetScanIgnore hides a found address from the suggestions (or shows it again).
func (c *Core) SetScanIgnore(ip string, ignored bool) error {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return errors.New("Ungültige Adresse.")
	}
	c.scanMu.Lock()
	defer c.scanMu.Unlock()
	list := slices.DeleteFunc(slices.Clone(c.scanIgnore), func(s string) bool { return s == a.String() })
	if ignored {
		list = append(list, a.String())
		slices.Sort(list)
	}
	if err := c.St.SetSetting(keyScanIgnore, list); err != nil {
		return err
	}
	c.scanIgnore = list
	return nil
}
