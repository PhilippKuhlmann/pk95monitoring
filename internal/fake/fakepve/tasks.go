package fakepve

import (
	"fmt"
	"slices"
	"strconv"
	"time"
)

type fakeTask struct {
	upid, node, typ, id, user, status string
	start, end                        time.Time
	log                               []string
}

func (t fakeTask) json() map[string]any {
	return map[string]any{"upid": t.upid, "node": t.node, "type": t.typ, "id": t.id, "user": t.user,
		"starttime": t.start.Unix(), "endtime": t.end.Unix(), "status": t.status}
}

func upid(node string, pid int, start time.Time, typ, id, user string) string {
	return fmt.Sprintf("UPID:%s:%08X:%08X:%08X:%s:%s:%s:", node, pid, pid*7+0x1000, start.Unix(), typ, id, user)
}

// lastBackupRun is the most recent 01:00 (local time) that is at least 45 minutes ago.
// Shortly after 01:00 that is yesterday's run, which would leave the 24 h window clients
// query (the nightly jobs would vanish between about 00:00 and 01:45), so the run is
// then moved to 23 h ago.
func lastBackupRun(now time.Time) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), 1, 0, 0, 0, now.Location())
	if now.Sub(t) < 45*time.Minute {
		t = t.AddDate(0, 0, -1)
	}
	if lim := now.Add(-23 * time.Hour).Truncate(time.Minute); t.Before(lim) {
		t = lim
	}
	return t
}

// tasks builds the task list: the nightly job "backup-nightly" with one vzdump task per node for
// the last 14 nights (tonight pve2's fails when backupFailed is set; a warning on pve1 4 nights
// ago, a failure on pve3 9 nights ago), the weekly job "backup-weekly" (PVE 8.3 logs its job
// ID; one task per node), a single backup of CT 201, an old failure and some other tasks.
// Newest first, like PVE. s.mu is held.
func (s *Server) tasks(now time.Time) []fakeTask {
	var out []fakeTask
	base := lastBackupRun(now)
	for night := range nightlyRuns {
		day := base.AddDate(0, 0, -night)
		for i, n := range s.nodes {
			start := day.Add(time.Duration(i) * 12 * time.Minute)
			var outcome func(vmid int) string
			switch {
			case night == 0 && s.backupFailed && n.name == "pve2":
				outcome = func(vmid int) string { return map[bool]string{true: "error"}[vmid == 103] }
			case night == 4 && n.name == "pve1":
				outcome = func(vmid int) string { return map[bool]string{true: "warn"}[vmid == 101] }
			case night == 9 && n.name == "pve3":
				outcome = func(vmid int) string { return map[bool]string{true: "error"}[vmid == 202] }
			}
			cmd := "vzdump --all 1 --exclude " + strconv.Itoa(notBackedUpVMID) + " --mode snapshot --storage backup --node " + n.name + " --compress zstd"
			out = append(out, s.backupTask(n.name, 0x3a00+i+night*16, start, "", cmd, func(g *guest) bool { return g.vmid != notBackedUpVMID }, outcome))
		}
	}
	// weekly on Sunday 03:00: VMs 101 (pve1) and 105 (pve3); a warning for 105 two weeks ago
	sunday := time.Date(now.Year(), now.Month(), now.Day(), 3, 0, 0, 0, now.Location())
	for sunday.Weekday() != time.Sunday || sunday.After(now.Add(-time.Hour)) {
		sunday = sunday.AddDate(0, 0, -1)
	}
	for week := range 5 {
		day := sunday.AddDate(0, 0, -7*week)
		for i, vm := range []struct {
			id   int
			node string
		}{{101, "pve1"}, {105, "pve3"}} {
			id := strconv.Itoa(vm.id)
			var outcome func(int) string
			if week == 2 && vm.id == 105 {
				outcome = func(int) string { return "warn" }
			}
			cmd := "vzdump " + id + " --mode snapshot --storage backup --notes-template '{{guestname}}' --job-id backup-weekly --node " + vm.node
			out = append(out, s.backupTask(vm.node, 0x6a00+i+week*16, day.Add(time.Duration(i)*time.Minute), id, cmd, func(g *guest) bool { return g.vmid == vm.id }, outcome))
		}
	}

	// A manual backup of CT 201 three hours ago and a failure of VM 105 two days ago.
	single := now.Truncate(time.Hour).Add(-3 * time.Hour)
	out = append(out,
		fakeTask{upid: upid("pve2", 0x4b10, single, "vzdump", "201", "root@pam"), node: "pve2", typ: "vzdump", id: "201",
			user: "root@pam", status: "OK", start: single, end: single.Add(70 * time.Second),
			log: []string{"INFO: Starting Backup of VM 201 (lxc)", "INFO: Finished Backup of VM 201 (00:01:10)", "TASK OK"}},
		fakeTask{upid: upid("pve3", 0x2c44, base.Add(-48*time.Hour), "vzdump", "105", "root@pam"), node: "pve3", typ: "vzdump", id: "105",
			user: "root@pam", status: "unable to connect to backup storage", start: base.Add(-48 * time.Hour), end: base.Add(-48*time.Hour + time.Minute)},
	)
	// Other tasks that the backup check must ignore.
	for i, x := range []struct{ node, typ, id string }{{"pve1", "qmstart", "104"}, {"pve3", "vncproxy", "105"}, {"pve2", "aptupdate", ""}} {
		start := now.Truncate(time.Minute).Add(-time.Duration(i+1) * 37 * time.Minute)
		out = append(out, fakeTask{upid: upid(x.node, 0x5000+i, start, x.typ, x.id, "root@pam"), node: x.node, typ: x.typ, id: x.id,
			user: "root@pam", status: "OK", start: start, end: start.Add(5 * time.Second)})
	}
	slices.SortFunc(out, func(a, b fakeTask) int { return b.start.Compare(a.start) })
	return out
}

// nightlyRuns is how many nights of the nightly job the task archive keeps.
const nightlyRuns = 14

// backupTask is a finished vzdump task on a node backing up the node's guests that pick
// selects; outcome tells per VMID "error", "warn" or "" (nil: all fine).
func (s *Server) backupTask(nodeName string, pid int, start time.Time, id, cmd string, pick func(*guest) bool, outcome func(int) string) fakeTask {
	t := fakeTask{node: nodeName, typ: "vzdump", id: id, user: "root@pam", status: "OK", start: start}
	t.upid = upid(nodeName, pid, start, "vzdump", id, t.user)
	t.log = []string{"INFO: starting new backup job: " + cmd}
	var guests []*guest
	for _, g := range s.guests {
		if g.node == nodeName && !g.template && pick(g) {
			guests = append(guests, g)
		}
	}
	slices.SortFunc(guests, func(a, b *guest) int { return a.vmid - b.vmid })
	cur := start
	failed, warned := false, 0
	for _, g := range guests {
		gid := strconv.Itoa(g.vmid)
		kind := "qemu"
		if g.typ == "lxc" {
			kind = "lxc"
		}
		t.log = append(t.log, "INFO: Starting Backup of VM "+gid+" ("+kind+")")
		cur = cur.Add(2*time.Minute + time.Duration(g.vmid%5)*20*time.Second)
		o := ""
		if outcome != nil {
			o = outcome(g.vmid)
		}
		switch o {
		case "error":
			failed = true
			t.log = append(t.log, "ERROR: Backup of VM "+gid+" failed - job failed with err -5 - Input/output error")
			continue
		case "warn":
			warned++
			t.log = append(t.log, "WARN: unable to read guest agent status of VM "+gid)
		}
		t.log = append(t.log, "INFO: Finished Backup of VM "+gid+" (00:02:"+fmt.Sprintf("%02d", g.vmid%60)+")")
	}
	t.end = cur.Add(20 * time.Second)
	switch {
	case failed:
		t.status = "job errors"
		t.log = append(t.log, "INFO: Backup job finished with errors", "TASK ERROR: job errors")
	case warned > 0:
		t.status = "WARNINGS: " + strconv.Itoa(warned)
		t.log = append(t.log, "INFO: Backup job finished successfully", "TASK WARNINGS: "+strconv.Itoa(warned))
	default:
		t.log = append(t.log, "INFO: Backup job finished successfully", "TASK OK")
	}
	return t
}

// backupJobs is /cluster/backup: the nightly job of all guests but the test VM, the weekly
// job of VMs 101 and 105 and a disabled job of the test VM. s.mu is held.
func (s *Server) backupJobs(now time.Time) []map[string]any {
	next := func(hour int, weekday time.Weekday, daily bool) int64 {
		t := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
		for !t.After(now) || !daily && t.Weekday() != weekday {
			t = t.AddDate(0, 0, 1)
		}
		return t.Unix()
	}
	return []map[string]any{
		{"id": "backup-nightly", "type": "vzdump", "enabled": 1, "schedule": "01:00", "storage": "backup", "all": 1,
			"exclude": strconv.Itoa(notBackedUpVMID), "mode": "snapshot", "comment": "Nachtsicherung", "next-run": next(1, 0, true)},
		{"id": "backup-weekly", "type": "vzdump", "enabled": 1, "schedule": "sun 03:00", "storage": "backup", "vmid": "101,105",
			"mode": "snapshot", "comment": "Wöchentlich Dateiserver und SQL", "next-run": next(3, time.Sunday, false)},
		{"id": "backup-test", "type": "vzdump", "enabled": 0, "schedule": "sat 22:00", "storage": "backup", "vmid": "106", "mode": "stop"},
	}
}

// notBackedUpVMID is the guest no backup job covers (the stopped test VM).
const notBackedUpVMID = 106

// notBackedUp is /cluster/backup-info/not-backed-up. s.mu is held.
func (s *Server) notBackedUp() []map[string]any {
	out := []map[string]any{}
	for _, g := range s.guests {
		if g.vmid == notBackedUpVMID {
			out = append(out, map[string]any{"vmid": g.vmid, "name": g.name, "type": g.typ})
		}
	}
	return out
}

// storageContent lists the backups of a storage: the shared storage "backup" keeps the
// nightly backups of the last 7 days of every guest except the test VM; a failing nightly
// job (SetBackupFailed) leaves out the latest backup of VM 103. s.mu is held.
func (s *Server) storageContent(n *node, name, content string, now time.Time) (any, int, string) {
	var st *storage
	for _, x := range s.storages {
		if x.name == name && (x.node == n.name || x.shared) {
			st = x
		}
	}
	if st == nil {
		return nil, 500, "storage '" + name + "' does not exist"
	}
	out := []map[string]any{}
	if name != "backup" || (content != "" && content != "backup") {
		return out, 200, ""
	}
	base := lastBackupRun(now)
	for _, g := range s.guests {
		if g.template || g.vmid == notBackedUpVMID {
			continue
		}
		kind, ext := "qemu", "vma.zst"
		if g.typ == "lxc" {
			kind, ext = "lxc", "tar.zst"
		}
		for day := range 7 {
			if day == 0 && s.backupFailed && g.vmid == 103 {
				continue
			}
			at := base.AddDate(0, 0, -day).Add(time.Duration(g.vmid%60) * time.Minute)
			if at.After(now) {
				continue
			}
			volid := fmt.Sprintf("backup:backup/vzdump-%s-%d-%s.%s", kind, g.vmid, at.Format("2006_01_02-15_04_05"), ext)
			out = append(out, map[string]any{"volid": volid, "vmid": g.vmid, "ctime": at.Unix(), "content": "backup", "format": ext,
				"subtype": kind, "size": int64(g.maxdisk * (0.18 + float64(g.vmid%7)*0.02))})
		}
	}
	return out, 200, ""
}
