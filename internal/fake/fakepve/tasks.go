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

// tasks builds the recent task list: a nightly vzdump job per node (pve2's fails when
// backupFailed is set), a single backup of CT 201, an old failure outside 24 h and some
// other tasks. Newest first, like PVE. s.mu is held.
func (s *Server) tasks(now time.Time) []fakeTask {
	var out []fakeTask
	base := lastBackupRun(now)
	for i, n := range s.nodes {
		start := base.Add(time.Duration(i) * 12 * time.Minute)
		t := fakeTask{node: n.name, typ: "vzdump", user: "root@pam", status: "OK", start: start}
		t.upid = upid(n.name, 0x3a00+i, start, "vzdump", "", t.user)
		t.log = []string{"INFO: starting new backup job: vzdump --all 1 --mode snapshot --storage backup --node " + n.name + " --compress zstd"}
		cur := start
		var guests []*guest
		for _, g := range s.guests {
			if g.node == n.name && !g.template {
				guests = append(guests, g)
			}
		}
		slices.SortFunc(guests, func(a, b *guest) int { return a.vmid - b.vmid })
		failed := false
		for _, g := range guests {
			id := strconv.Itoa(g.vmid)
			kind := "qemu"
			if g.typ == "lxc" {
				kind = "lxc"
			}
			t.log = append(t.log, "INFO: Starting Backup of VM "+id+" ("+kind+")")
			cur = cur.Add(2*time.Minute + time.Duration(g.vmid%5)*20*time.Second)
			if s.backupFailed && n.name == "pve2" && g.vmid == 103 {
				failed = true
				t.log = append(t.log, "ERROR: Backup of VM "+id+" failed - job failed with err -5 - Input/output error")
				continue
			}
			t.log = append(t.log, "INFO: Finished Backup of VM "+id+" (00:02:"+fmt.Sprintf("%02d", g.vmid%60)+")")
		}
		t.end = cur.Add(20 * time.Second)
		if failed {
			t.status = "job errors"
			t.log = append(t.log, "INFO: Backup job finished with errors", "TASK ERROR: job errors")
		} else {
			t.log = append(t.log, "INFO: Backup job finished successfully", "TASK OK")
		}
		out = append(out, t)
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
