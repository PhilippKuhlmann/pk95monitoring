package fakepve

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Simulated hardware and cluster health: SMART data of the disks, zpool status, storage
// replication, HA resources, Ceph (not installed unless SetCephHealth) and certificates.

type replJob struct {
	id, source, target, schedule string
	guest                        int
	every                        time.Duration // schedule as interval
	failCount                    int
	err                          string
}

type cephState struct {
	status, message string
}

type cert struct {
	file, issuer string
	san          []string
	notAfter     time.Time
}

const day = 24 * time.Hour

// seedHealth adds the health data of the demo cluster; s.disks and s.pools exist.
func (s *Server) seedHealth(now time.Time) {
	for i, d := range s.disks {
		switch d.typ {
		case "nvme":
			d.temp, d.hours, d.spare, d.unsafe = 38+float64(i%3)*3, 9200+i*311, 100, 7+i
			d.writtenTB = 12.4 + float64(i)*3.1
		case "ssd":
			d.temp, d.hours, d.cycles = 33+float64(i%2)*2, 21000+i*1700, 40+i
		default: // hdd
			d.temp, d.hours, d.cycles = 35+float64(i%4), 26300+i*800, 61+i
		}
	}
	// One WD Red with pending sectors: the warn of a dying disk.
	if d := s.findDisk("pve3", "sdc"); d != nil {
		d.pending, d.realloc = 2, 8
	}
	for _, p := range s.pools {
		p.cksum, p.faulted = map[string]int{}, map[string]bool{}
		if len(p.leaves) > 0 {
			continue
		}
		for _, d := range s.disks {
			if d.node == p.node && d.typ != "hdd" {
				part := d.dev + "3"
				if d.typ == "nvme" {
					part = d.dev + "p3"
				}
				p.leaves = append(p.leaves, part)
			}
		}
		if len(p.leaves) < 2 {
			p.layout = "" // a single disk
		}
	}
	s.repl = []*replJob{
		{id: "100-0", guest: 100, source: "pve1", target: "pve2", schedule: "*/15", every: 15 * time.Minute},
		{id: "102-0", guest: 102, source: "pve2", target: "pve1", schedule: "hourly", every: time.Hour},
		{id: "105-0", guest: 105, source: "pve3", target: "pve1", schedule: "*/30", every: 30 * time.Minute},
	}
	s.ha = map[string]string{"vm:100": "started", "vm:102": "started", "ct:201": "started"}
	s.certs = map[string][]cert{}
	s.iowait = map[string]float64{}
	for i, n := range s.nodes {
		s.certs[n.name] = []cert{
			{file: "pve-root-ca.pem", issuer: "/CN=Proxmox Virtual Environment/OU=demo/O=PVE Cluster Manager CA", notAfter: now.Add(3200 * day)},
			{file: "pve-ssl.pem", issuer: "/CN=Proxmox Virtual Environment/OU=demo/O=PVE Cluster Manager CA", san: []string{n.name, n.ip}, notAfter: now.Add(time.Duration(500+40*i) * day)},
		}
	}
	s.certs["pve1"] = append(s.certs["pve1"], cert{file: "pveproxy-ssl.pem", issuer: "/C=US/O=Let's Encrypt/CN=R11", san: []string{"pve1.demo.example"}, notAfter: now.Add(64 * day)})
}

// ioWaitAt is the IO delay of a node (fraction): small, with a daily bump (backups).
func (s *Server) ioWaitAt(n *node, now time.Time) float64 {
	if v, ok := s.iowait[n.name]; ok {
		return v
	}
	return round4(clamp(0.006+0.006*wave(now, 24*time.Hour, n.phase+2)+noise(0.002), 0, 1))
}

func round4(v float64) float64 { return float64(int(v*10000+0.5)) / 10000 }

// smart is GET /nodes/{node}/disks/smart?disk=/dev/…
func (s *Server) smart(n *node, devPath string) (any, int, string) {
	for _, d := range s.disks {
		if d.node != n.name || "/dev/"+d.dev != devPath {
			continue
		}
		if d.typ == "nvme" {
			used := 0
			if d.wearout >= 0 {
				used = 100 - d.wearout
			}
			txt := fmt.Sprintf(`SMART/Health Information (NVMe Log 0x02)
Critical Warning:                   0x00
Temperature:                        %d Celsius
Available Spare:                    %d%%
Available Spare Threshold:          10%%
Percentage Used:                    %d%%
Data Units Read:                    %s [%.2f TB]
Data Units Written:                 %s [%.2f TB]
Power Cycles:                       %d
Power On Hours:                     %s
Unsafe Shutdowns:                   %d
Media and Data Integrity Errors:    %d
Error Information Log Entries:      0
`, int(d.temp), d.spare, used, thousands(int(d.writtenTB*1.6*1e12/512000)), d.writtenTB*1.6, thousands(int(d.writtenTB*1e12/512000)), d.writtenTB,
				d.cycles+30, thousands(d.hours), d.unsafe, d.mediaErrors)
			return map[string]any{"health": d.health, "type": "text", "text": txt}, 200, ""
		}
		attr := func(id int, name string, value, threshold int, raw string) map[string]any {
			return map[string]any{"id": fmt.Sprintf("%3d", id), "name": name, "value": value, "worst": value, "threshold": threshold,
				"raw": raw, "fail": "-", "flags": "PO--CK", "normalized": value}
		}
		attrs := []map[string]any{
			attr(1, "Raw_Read_Error_Rate", 200, 51, "0"),
			attr(5, "Reallocated_Sector_Ct", 200-d.realloc, 140, strconv.Itoa(d.realloc)),
			attr(9, "Power_On_Hours", 70, 0, strconv.Itoa(d.hours)),
			attr(12, "Power_Cycle_Count", 100, 0, strconv.Itoa(d.cycles)),
			attr(194, "Temperature_Celsius", 115-int(d.temp), 0, fmt.Sprintf("%d (Min/Max 21/%d)", int(d.temp), int(d.temp)+9)),
			attr(197, "Current_Pending_Sector", 200, 0, strconv.Itoa(d.pending)),
			attr(198, "Offline_Uncorrectable", 100, 0, strconv.Itoa(d.uncorrect)),
			attr(199, "UDMA_CRC_Error_Count", 200, 0, strconv.Itoa(d.crc)),
		}
		return map[string]any{"health": d.health, "type": "ata", "attributes": attrs}, 200, ""
	}
	return nil, 500, "no such disk '" + devPath + "'"
}

func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// zfsDetail is GET /nodes/{node}/disks/zfs/{name}.
func (s *Server) zfsDetail(n *node, name string, now time.Time) (any, int, string) {
	p := s.findPool(n.name, name)
	if p == nil {
		return nil, 500, "zpool '" + name + "' does not exist"
	}
	state := p.health
	var leaves []map[string]any
	for _, l := range p.leaves {
		st := "ONLINE"
		if p.faulted[l] {
			st, state = "FAULTED", "DEGRADED"
		}
		leaves = append(leaves, map[string]any{"name": l, "state": st, "read": 0, "write": 0, "cksum": p.cksum[l], "msg": "", "leaf": 1})
	}
	children := leaves
	if p.layout != "" {
		children = []map[string]any{{"name": p.layout, "state": state, "read": 0, "write": 0, "cksum": 0, "msg": "", "children": leaves}}
	}
	out := map[string]any{"name": p.name, "state": state, "children": children, "errors": "No known data errors",
		"scan": "scrub repaired 0B in 00:21:07 with 0 errors on " + p.scrubAt.Format("Mon Jan _2 15:04:05 2006")}
	if p.dataErrors != "" {
		out["errors"] = p.dataErrors
	}
	if state != "ONLINE" {
		out["status"] = "One or more devices are faulted in response to persistent errors. Sufficient replicas exist for the pool to continue functioning in a degraded state."
		out["action"] = "Replace the faulted device, or use 'zpool clear' to mark the device repaired."
	}
	return out, 200, ""
}

// replication is GET /nodes/{node}/replication: the jobs whose source is the node.
func (s *Server) replication(n *node, now time.Time) []map[string]any {
	out := []map[string]any{}
	for _, j := range s.repl {
		if j.source != n.name {
			continue
		}
		last := now.Truncate(j.every)
		e := map[string]any{"id": j.id, "guest": j.guest, "jobnum": 0, "target": j.target, "type": "local", "schedule": j.schedule,
			"last_sync": last.Unix(), "next_sync": last.Add(j.every).Unix(), "duration": 3.2, "fail_count": j.failCount}
		if j.failCount > 0 {
			e["last_sync"] = last.Add(-time.Duration(j.failCount) * j.every).Unix()
			e["error"] = j.err
		}
		out = append(out, e)
	}
	return out
}

// haStatus is GET /cluster/ha/status/current.
func (s *Server) haStatus() []map[string]any {
	out := []map[string]any{{"id": "quorum", "type": "quorum", "status": "OK", "quorate": 1}}
	if len(s.ha) == 0 {
		return out
	}
	out = append(out, map[string]any{"id": "master", "type": "master", "node": "pve1", "status": "pve1 (active)"})
	sids := make([]string, 0, len(s.ha))
	for sid := range s.ha {
		sids = append(sids, sid)
	}
	slices.Sort(sids)
	for _, sid := range sids {
		vmid, _ := strconv.Atoi(strings.SplitN(sid, ":", 2)[1])
		nodeName := "pve1"
		for _, g := range s.guests {
			if g.vmid == vmid {
				nodeName = g.node
			}
		}
		out = append(out, map[string]any{"id": "service:" + sid, "sid": sid, "type": "service", "node": nodeName, "state": s.ha[sid],
			"crm_state": s.ha[sid], "request_state": "started", "status": sid + " (" + nodeName + ", " + s.ha[sid] + ")"})
	}
	return out
}

// cephStatus is GET /cluster/ceph/status (s.ceph is set).
func (s *Server) cephStatus() map[string]any {
	checks := map[string]any{}
	if s.ceph.message != "" {
		checks["DEMO_CHECK"] = map[string]any{"severity": s.ceph.status, "summary": map[string]any{"message": s.ceph.message}}
	}
	return map[string]any{"health": map[string]any{"status": s.ceph.status, "checks": checks},
		"pgmap": map[string]any{"bytes_used": int64(3 * tib), "bytes_total": int64(12 * tib)}}
}

// certInfo is GET /nodes/{node}/certificates/info.
func (s *Server) certInfo(n *node) []map[string]any {
	out := []map[string]any{}
	for _, c := range s.certs[n.name] {
		out = append(out, map[string]any{"filename": c.file, "issuer": c.issuer, "subject": "/CN=" + n.name, "san": c.san,
			"notafter": c.notAfter.Unix(), "notbefore": c.notAfter.Add(-90 * day).Unix()})
	}
	return out
}

// ---- test controls ----

func (s *Server) findDisk(nodeName, dev string) *disk {
	for _, d := range s.disks {
		if d.node == nodeName && d.dev == dev {
			return d
		}
	}
	return nil
}

func (s *Server) findPool(nodeName, name string) *pool {
	for _, p := range s.pools {
		if p.node == nodeName && p.name == name {
			return p
		}
	}
	return nil
}

// SetDiskSMART sets the temperature and the pending and reallocated sectors of a disk.
func (s *Server) SetDiskSMART(nodeName, dev string, temp float64, pending, realloc int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := s.findDisk(nodeName, dev); d != nil {
		d.temp, d.pending, d.realloc = temp, pending, realloc
	}
}

// SetVdevFaulted marks a pool member FAULTED (the pool DEGRADED) with checksum errors.
func (s *Server) SetVdevFaulted(nodeName, poolName, leaf string, cksum int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.findPool(nodeName, poolName); p != nil {
		p.faulted[leaf], p.cksum[leaf], p.health = true, cksum, "DEGRADED"
	}
}

// SetScrubAge sets when the last scrub of a pool finished.
func (s *Server) SetScrubAge(nodeName, poolName string, age time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.findPool(nodeName, poolName); p != nil {
		p.scrubAt = s.now().Add(-age)
	}
}

// SetReplicationFailed lets a replication job fail with msg (empty: works again).
func (s *Server) SetReplicationFailed(id, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.repl {
		if j.id == id {
			if msg == "" {
				j.failCount, j.err = 0, ""
			} else {
				j.failCount, j.err = j.failCount+1, msg
			}
		}
	}
}

// SetHAState sets the state of an HA resource ("vm:100" → "error").
func (s *Server) SetHAState(sid, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ha[sid] = state
}

// SetCephHealth installs Ceph with a health status ("HEALTH_WARN") and message; "" removes it.
func (s *Server) SetCephHealth(status, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if status == "" {
		s.ceph = nil
		return
	}
	s.ceph = &cephState{status: status, message: msg}
}

// SetCertExpiry sets when the web certificate of a node (pveproxy-ssl.pem, else pve-ssl.pem) expires.
func (s *Server) SetCertExpiry(nodeName string, in time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.certs[nodeName]
	for _, f := range []string{"pveproxy-ssl.pem", "pve-ssl.pem"} {
		if i := slices.IndexFunc(list, func(c cert) bool { return c.file == f }); i >= 0 {
			list[i].notAfter = s.now().Add(in)
			return
		}
	}
}

// SetIOWait fixes the IO delay of a node (fraction 0–1).
func (s *Server) SetIOWait(nodeName string, frac float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.iowait[nodeName] = frac
}
