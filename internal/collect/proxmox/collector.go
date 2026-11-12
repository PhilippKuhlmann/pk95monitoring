package proxmox

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"pk95monitoring/internal/model"
)

const (
	configTTL = 10 * time.Minute // guest configs ("Start at boot")
	aptTTL    = time.Hour        // pending apt updates per node
)

// Collector polls Proxmox VE devices. It keeps per-device state between polls (previous
// guest counters for rates, cached guest configs and apt update counts) and is safe for
// concurrent use for different devices.
type Collector struct {
	mu      sync.Mutex
	now     func() time.Time
	clients map[string]*client
	devs    map[string]*devState
}

type devState struct {
	counters map[string]counterSample // guest instance (vm/100) -> last counters
	onboot   map[string]onbootEntry   // qemu/100 -> onboot
	apt      map[string]aptEntry      // node -> update counts
	taskLogs map[string]taskInfo      // logKey -> parsed vzdump log
	smart    map[string]smartEntry    // node \x00 /dev/… -> SMART data
	certs    map[string]certEntry     // node -> certificates
}

type counterSample struct {
	at time.Time
	v  [4]float64 // netin, netout, diskread, diskwrite
}

type onbootEntry struct {
	on bool
	at time.Time
}

type aptEntry struct {
	pending, security int
	err               error
	at                time.Time
}

// New returns a Collector.
func New() *Collector {
	return &Collector{now: time.Now, clients: map[string]*client{}, devs: map[string]*devState{}}
}

// Forget drops the state of a device (deleted or changed).
func (c *Collector) Forget(deviceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cl := c.clients[deviceID]; cl != nil {
		cl.close()
	}
	delete(c.clients, deviceID)
	delete(c.devs, deviceID)
}

// clientFor returns the cached client of a device; a changed configuration starts over.
func (c *Collector) clientFor(d model.Device) *client {
	c.mu.Lock()
	defer c.mu.Unlock()
	sig := signature(d.Host, *d.Proxmox)
	if cl := c.clients[d.ID]; cl != nil {
		if cl.sig == sig {
			return cl
		}
		cl.close()
		delete(c.devs, d.ID)
	}
	cl := newClient(d.Host, *d.Proxmox)
	c.clients[d.ID] = cl
	return cl
}

// dev returns the state of a device; c.mu must be held.
func (c *Collector) dev(id string) *devState {
	st := c.devs[id]
	if st == nil {
		st = &devState{counters: map[string]counterSample{}, onboot: map[string]onbootEntry{}, apt: map[string]aptEntry{}, taskLogs: map[string]taskInfo{},
			smart: map[string]smartEntry{}, certs: map[string]certEntry{}}
		c.devs[id] = st
	}
	return st
}

// inventory is /cluster/resources split by type.
type inventory struct {
	nodes    []resource // sorted by name
	guests   []resource // VMs and containers without templates, sorted by VMID
	storages []resource
	online   map[string]bool
	ips      map[string]string
	names    map[string]string // VMID -> guest name
}

func newInventory(res []resource) inventory {
	inv := inventory{online: map[string]bool{}, ips: map[string]string{}, names: map[string]string{}}
	for _, r := range res {
		switch r.Type {
		case "node":
			inv.nodes = append(inv.nodes, r)
			inv.online[r.Node] = r.Status == "online"
		case "qemu", "lxc":
			inv.names[r.vmid()] = r.Name
			if r.Template != 1 {
				inv.guests = append(inv.guests, r)
			}
		case "storage":
			inv.storages = append(inv.storages, r)
		}
	}
	slices.SortFunc(inv.nodes, func(a, b resource) int { return strings.Compare(a.Node, b.Node) })
	slices.SortFunc(inv.guests, func(a, b resource) int { return cmp.Compare(a.VMID, b.VMID) })
	slices.SortFunc(inv.storages, func(a, b resource) int {
		return cmp.Or(strings.Compare(a.Storage, b.Storage), strings.Compare(a.Node, b.Node))
	})
	return inv
}

// applyClusterStatus prefers the online flags of the corosync view.
func (inv *inventory) applyClusterStatus(cs []clusterEntry) {
	for _, e := range cs {
		if e.Type != "node" {
			continue
		}
		if _, ok := inv.online[e.Name]; ok {
			inv.online[e.Name] = e.Online == 1
		}
		inv.ips[e.Name] = e.IP
	}
}

func (inv inventory) onlineCount() int {
	n := 0
	for _, on := range inv.online {
		if on {
			n++
		}
	}
	return n
}

// Collect polls one Proxmox VE device (a single node or a whole cluster). It returns an
// error only when the API cannot be reached or the token is rejected; partial failures
// become check states.
func (c *Collector) Collect(ctx context.Context, d model.Device) ([]model.Check, error) {
	if d.Proxmox == nil || strings.TrimSpace(d.Proxmox.TokenID) == "" {
		return nil, errors.New("Proxmox-Zugang fehlt (Token-ID und Secret)")
	}
	if strings.TrimSpace(d.Host) == "" {
		return nil, errors.New("Host fehlt")
	}
	cl := c.clientFor(d)
	ver, err := cl.version(ctx)
	if err != nil {
		return nil, friendly(err)
	}
	res, err := cl.resources(ctx)
	if err != nil {
		return nil, friendly(err)
	}
	inv := newInventory(res)
	if len(inv.nodes) == 0 {
		return nil, errNoNodes
	}
	cs, csErr := cl.clusterStatus(ctx)
	inv.applyClusterStatus(cs)
	now := c.now()
	details := c.nodeDetails(ctx, cl, d.ID, inv, now)

	checks := []model.Check{clusterCheck(ver, cs, csErr, inv)}
	// HA and Ceph only when used: an error (not configured, no permission) leaves them out.
	if ha, err := cl.haStatus(ctx); err == nil {
		if ch, ok := haCheck(ha); ok {
			checks = append(checks, ch)
		}
	}
	if ce, err := cl.ceph(ctx); err == nil && ce.Health.Status != "" {
		checks = append(checks, cephCheck(ce))
	}
	for _, n := range inv.nodes {
		checks = append(checks, nodeCheck(n, inv, details[n.Node]))
	}
	checks = append(checks, c.guestChecks(ctx, cl, d, inv, now)...)
	checks = append(checks, storageChecks(inv)...)
	for _, n := range inv.nodes {
		if det := details[n.Node]; det != nil {
			checks = append(checks, zfsChecks(n.Node, det.zfs, det.zfsDetails, now)...)
			checks = append(checks, diskChecks(n.Node, det.disks, det.smart)...)
			checks = append(checks, replicationChecks(n.Node, det.repl, inv.names, now)...)
			if ch, ok := certCheck(n.Node, det.certs.list, now); ok && det.certs.err == nil {
				checks = append(checks, ch)
			}
			// Without Sys.Modify (PVEAuditor only) the check is left out and the node shows
			// a hint, so the recommended read-only token does not leave the device unknown.
			if !isStatus(det.apt.err, http.StatusForbidden) {
				checks = append(checks, updatesCheck(n.Node, det.apt))
			}
		}
	}
	checks = append(checks, c.backupCheck(ctx, cl, d.ID, inv, details, now))
	c.prune(d.ID, inv)
	return checks, nil
}

// prune drops cached guest configs and apt counts of guests and nodes that are gone.
func (c *Collector) prune(devID string, inv inventory) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.dev(devID)
	guests := map[string]bool{}
	for _, g := range inv.guests {
		guests[g.Type+"/"+g.vmid()] = true
	}
	for k := range st.onboot {
		if !guests[k] {
			delete(st.onboot, k)
		}
	}
	for node := range st.apt {
		if _, ok := inv.online[node]; !ok {
			delete(st.apt, node)
		}
	}
	for node := range st.certs {
		if _, ok := inv.online[node]; !ok {
			delete(st.certs, node)
		}
	}
	for key := range st.smart {
		if _, ok := inv.online[strings.SplitN(key, "\x00", 2)[0]]; !ok {
			delete(st.smart, key)
		}
	}
}

var errNoNodes = errors.New("Token ungültig oder ohne Rechte (keine Knoten sichtbar – Rolle PVEAuditor auf / vergeben?)")

// nodeDetail holds the per-node calls of one poll.
type nodeDetail struct {
	status    nodeStatus
	statusErr error
	zfs       []zfsPool
	zfsErr    error
	disks     []disk
	disksErr  error
	apt       aptEntry
	tasks     []task // vzdump tasks of the backup window
	tasksErr  error
	// health details (errors leave them out: older PVE, no permission, not configured)
	zfsDetails map[string]zfsDetail  // pool -> zpool status
	smart      map[string]smartEntry // /dev/… -> SMART data
	repl       []replJob
	certs      certEntry
}

// nodeDetails queries all online nodes in parallel.
func (c *Collector) nodeDetails(ctx context.Context, cl *client, devID string, inv inventory, now time.Time) map[string]*nodeDetail {
	out := map[string]*nodeDetail{}
	since := now.Add(-backupWindow).Unix()
	var wg sync.WaitGroup
	for _, n := range inv.nodes {
		if !inv.online[n.Node] {
			continue
		}
		det := &nodeDetail{}
		out[n.Node] = det
		wg.Go(func() {
			det.status, det.statusErr = cl.nodeStatus(ctx, n.Node)
			det.zfs, det.zfsErr = cl.zfsPools(ctx, n.Node)
			det.zfsDetails = map[string]zfsDetail{}
			for _, p := range det.zfs {
				if zd, err := cl.zfsDetail(ctx, n.Node, p.Name); err == nil {
					det.zfsDetails[p.Name] = zd
				}
			}
			det.disks, det.disksErr = cl.disks(ctx, n.Node)
			det.smart = map[string]smartEntry{}
			for _, d := range det.disks {
				if d.DevPath != "" {
					det.smart[d.DevPath] = c.smartFor(ctx, cl, devID, n.Node, d.DevPath, now)
				}
			}
			if jobs, err := cl.replication(ctx, n.Node); err == nil {
				det.repl = jobs
			}
			det.certs = c.certsFor(ctx, cl, devID, n.Node, now)
			det.apt = c.aptFor(ctx, cl, devID, n.Node, now)
			det.tasks, det.tasksErr = cl.backupTasks(ctx, n.Node, since)
		})
	}
	wg.Wait()
	return out
}

// aptFor returns the pending updates of a node, cached for an hour (also a missing permission).
func (c *Collector) aptFor(ctx context.Context, cl *client, devID, node string, now time.Time) aptEntry {
	c.mu.Lock()
	e, ok := c.dev(devID).apt[node]
	c.mu.Unlock()
	if ok && now.Sub(e.at) < aptTTL {
		return e
	}
	pkgs, err := cl.aptUpdates(ctx, node)
	e = aptEntry{err: err, at: now}
	if err == nil {
		e.pending = len(pkgs)
		for _, p := range pkgs {
			if strings.Contains(p.Origin, "Security") {
				e.security++
			}
		}
	}
	if err == nil || isStatus(err, http.StatusForbidden) {
		c.mu.Lock()
		c.dev(devID).apt[node] = e
		c.mu.Unlock()
	}
	return e
}

func clusterCheck(ver version, cs []clusterEntry, csErr error, inv inventory) model.Check {
	total, online := len(inv.nodes), inv.onlineCount()
	ch := model.Check{Key: "pve:cluster", Name: "Cluster", Group: "Cluster", Facts: map[string]string{
		"PVE-Version": ver.Version,
		"Knoten":      strconv.Itoa(total),
	}}
	nodes := strconv.Itoa(online) + " von " + strconv.Itoa(total) + " Knoten online"
	if csErr != nil {
		ch.State = model.StateUnknown
		ch.Message = permMessage(csErr, "Sys.Audit auf /") + " – " + nodes
		return ch
	}
	var cluster *clusterEntry
	for i := range cs {
		if cs[i].Type == "cluster" {
			cluster = &cs[i]
		}
	}
	switch {
	case cluster == nil: // standalone node
		ch.Facts["Cluster"] = "Einzelknoten"
		ch.State, ch.Message = model.StateOK, "Einzelknoten online"
		if online < total {
			ch.State, ch.Message = model.StateCrit, nodes
		}
	case cluster.Quorate != 1:
		ch.Facts["Cluster"] = cluster.Name
		ch.State, ch.Message = model.StateCrit, "Kein Quorum – "+nodes
	case online < total:
		ch.Facts["Cluster"] = cluster.Name
		ch.State, ch.Message = model.StateWarn, nodes+" (Quorum vorhanden)"
	default:
		ch.Facts["Cluster"] = cluster.Name
		ch.State, ch.Message = model.StateOK, "Quorum vorhanden, "+nodes
	}
	return ch
}

func nodeCheck(n resource, inv inventory, det *nodeDetail) model.Check {
	ch := model.Check{Key: "pve:node:" + n.Node, Name: "Knoten " + n.Node, Group: "Knoten", Facts: map[string]string{}}
	if ip := inv.ips[n.Node]; ip != "" {
		ch.Facts["IP"] = ip
	}
	if !inv.online[n.Node] || det == nil {
		ch.State, ch.Message = model.StateCrit, "Knoten offline"
		return ch
	}
	// /cluster/resources has the basics; /nodes/{n}/status adds load, swap and facts.
	cpu, memUsed, memTotal := float64(n.CPU), float64(n.Mem), float64(n.MaxMem)
	rootUsed, rootTotal, uptime := float64(n.Disk), float64(n.MaxDisk), float64(n.Uptime)
	var extra []model.Metric
	msg := []string{"online"}
	if det.statusErr == nil {
		s := det.status
		cpu, memUsed, memTotal = float64(s.CPU), float64(s.Memory.Used), float64(s.Memory.Total)
		rootUsed, rootTotal, uptime = float64(s.RootFS.Used), float64(s.RootFS.Total), float64(s.Uptime)
		if len(s.LoadAvg) > 0 {
			extra = append(extra, model.Metric{Name: "cpu.load1", Instance: n.Node, Value: round2(float64(s.LoadAvg[0]))})
		}
		if s.Swap.Total > 0 {
			extra = append(extra, model.Metric{Name: "swap.usage", Instance: n.Node, Value: round2(pct(float64(s.Swap.Used), float64(s.Swap.Total))), Unit: "%"})
		}
		// IO delay: share of time the CPUs waited for disks (slow or overloaded storage)
		extra = append(extra, model.Metric{Name: "cpu.iowait", Instance: n.Node, Value: round2(float64(s.Wait) * 100), Unit: "%"})
		nodeFacts(ch.Facts, s)
	} else {
		ch.Facts["Hinweis"] = permMessage(det.statusErr, "Sys.Audit auf /nodes")
	}
	if det.zfsErr != nil && !isStatus(det.zfsErr, 501) {
		ch.Facts["Hinweis ZFS"] = permMessage(det.zfsErr, "Sys.Audit auf /nodes")
	}
	if det.disksErr != nil {
		ch.Facts["Hinweis Platten"] = permMessage(det.disksErr, "Sys.Audit auf /nodes")
	}
	if isStatus(det.apt.err, http.StatusForbidden) {
		ch.Facts["Hinweis Updates"] = permMessage(det.apt.err, "Sys.Modify auf /nodes")
	}
	ch.State = model.StateOK
	ch.Metrics = []model.Metric{
		{Name: "cpu.usage", Instance: n.Node, Value: round2(cpu * 100), Unit: "%"},
		{Name: "mem.usage", Instance: n.Node, Value: round2(pct(memUsed, memTotal)), Unit: "%"},
		{Name: "mem.used", Instance: n.Node, Value: memUsed, Unit: "B"},
		{Name: "mem.total", Instance: n.Node, Value: memTotal, Unit: "B"},
	}
	if rootTotal > 0 {
		ch.Metrics = append(ch.Metrics, model.Metric{Name: "disk.usage", Instance: n.Node, Value: round2(pct(rootUsed, rootTotal)), Unit: "%"})
	}
	ch.Metrics = append(ch.Metrics, model.Metric{Name: "uptime", Instance: n.Node, Value: uptime, Unit: "s"})
	ch.Metrics = append(ch.Metrics, extra...)
	msg = append(msg, "CPU "+fmtPct(cpu*100), "RAM "+fmtPct(pct(memUsed, memTotal)))
	if det.statusErr == nil && len(det.status.LoadAvg) > 0 {
		msg = append(msg, "Last "+fmtFloat(float64(det.status.LoadAvg[0])))
	}
	if det.statusErr == nil && det.status.Wait >= 0.05 {
		msg = append(msg, "IO-Wartezeit "+fmtPct(float64(det.status.Wait)*100))
	}
	if uptime > 0 {
		msg = append(msg, fmtSince(uptime))
	}
	ch.Message = strings.Join(msg, " · ")
	return ch
}

func nodeFacts(f map[string]string, s nodeStatus) {
	if m := strings.TrimSpace(s.CPUInfo.Model); m != "" {
		if s.CPUInfo.Sockets > 1 {
			m = strconv.Itoa(int(s.CPUInfo.Sockets)) + " × " + m
		}
		f["CPU"] = m
	}
	if s.CPUInfo.CPUs > 0 {
		f["Kerne"] = strconv.Itoa(int(s.CPUInfo.CPUs))
	}
	if s.Memory.Total > 0 {
		f["Arbeitsspeicher"] = fmtBytes(float64(s.Memory.Total))
	}
	// "pve-manager/8.3.2/3e76eec21c4a14a7"
	if parts := strings.Split(s.PVEVersion, "/"); len(parts) >= 2 {
		f["PVE-Version"] = parts[1]
	}
	// "Linux 6.8.12-5-pve #1 SMP PREEMPT_DYNAMIC …"
	if fields := strings.Fields(s.KVersion); len(fields) >= 2 && fields[0] == "Linux" {
		f["Kernel"] = fields[1]
	} else if s.KVersion != "" {
		f["Kernel"] = s.KVersion
	}
}
