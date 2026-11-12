package proxmox

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"pk95monitoring/internal/model"
)

// backupWindow is how far back vzdump tasks are evaluated.
const backupWindow = 24 * time.Hour

// maxTaskLogs bounds the task logs read per poll. Parsed logs are cached per device (a
// finished task's log does not change), so later polls only read the logs of new tasks.
const maxTaskLogs = 10

// backupCheck evaluates the vzdump tasks of the last 24 hours, read per node with the node
// details. A failed guest stays crit until a later task backed it up; a failure without
// known guests (or a warning) until a later run of the same job succeeded. Another backup
// job on the same node does not clear it.
func (c *Collector) backupCheck(ctx context.Context, cl *client, devID string, inv inventory, details map[string]*nodeDetail, now time.Time) model.Check {
	ch := model.Check{Key: "pve:backup", Name: "Sicherungen", Group: "Sicherungen"}
	var all []task
	var badNodes []string
	var firstErr error
	readable := 0
	for _, n := range inv.nodes {
		det := details[n.Node]
		switch {
		case det == nil: // offline
		case det.tasksErr != nil:
			badNodes = append(badNodes, n.Node)
			firstErr = cmp.Or(firstErr, det.tasksErr)
		default:
			readable++
			all = append(all, det.tasks...)
		}
	}
	if readable == 0 && firstErr != nil {
		ch.State, ch.Message = model.StateUnknown, permMessage(firstErr, "Sys.Audit auf /nodes")
		return ch
	}

	since := now.Add(-backupWindow).Unix()
	var ts []task
	var lastOK float64
	for _, t := range all {
		if t.Type != "vzdump" || int64(t.StartTime) < since || t.EndTime == 0 || t.Status == "" {
			continue // other task, too old or still running
		}
		ts = append(ts, t)
		if taskOK(t.Status) && float64(t.EndTime) > lastOK {
			lastOK = float64(t.EndTime)
		}
	}
	slices.SortFunc(ts, func(a, b task) int {
		return cmp.Or(cmp.Compare(a.StartTime, b.StartTime), strings.Compare(a.UPID, b.UPID))
	})

	ch.Facts = map[string]string{"Sicherungen (24 h)": strconv.Itoa(len(ts))}
	if lastOK > 0 {
		ch.Facts["Letzte Sicherung"] = fmtTime(lastOK)
	}
	if len(badNodes) > 0 {
		ch.Facts["Hinweis"] = "Aufgaben von " + strings.Join(badNodes, ", ") + " nicht lesbar: " + permMessage(firstErr, "Sys.Audit auf /nodes")
	}
	if len(ts) == 0 {
		ch.State, ch.Message = model.StateOK, "Keine Sicherung in den letzten 24 h"
		return ch
	}

	failed, warned := evalBackups(ts, c.taskInfoFunc(ctx, cl, devID, ts), inv.names)
	switch {
	case len(failed) > 0:
		ch.State, ch.Message = model.StateCrit, "Sicherung fehlgeschlagen: "+strings.Join(failed, ", ")
		if len(warned) > 0 {
			ch.Message += "; mit Warnungen: " + strings.Join(warned, ", ")
		}
	case len(warned) > 0:
		ch.State, ch.Message = model.StateWarn, "Sicherung mit Warnungen: "+strings.Join(warned, ", ")
	default:
		ch.State, ch.Message = model.StateOK, "Alle Sicherungen erfolgreich"
		if lastOK > 0 {
			ch.Message += ", letzte " + fmtTime(lastOK)
		}
	}
	return ch
}

// taskInfoFunc returns a lookup of the parsed log of a task. Single-guest tasks (ID set) need
// no log. Job logs come from the device's cache or are read (at most maxTaskLogs per poll);
// the cache is pruned to the tasks in ts.
func (c *Collector) taskInfoFunc(ctx context.Context, cl *client, devID string, ts []task) func(task) (taskInfo, bool) {
	keep := make(map[string]bool, len(ts))
	for _, t := range ts {
		keep[logKey(t)] = true
	}
	c.mu.Lock()
	cache := c.dev(devID).taskLogs
	for k := range cache {
		if !keep[k] {
			delete(cache, k)
		}
	}
	c.mu.Unlock()

	tried := map[string]bool{}
	reads := 0
	return func(t task) (taskInfo, bool) {
		if t.ID != "" {
			if taskOK(t.Status) {
				return taskInfo{finished: []string{t.ID}}, true
			}
			return taskInfo{failed: []string{t.ID}}, true
		}
		key := logKey(t)
		c.mu.Lock()
		ti, ok := c.dev(devID).taskLogs[key]
		c.mu.Unlock()
		if ok || tried[key] || reads >= maxTaskLogs {
			return ti, ok
		}
		tried[key] = true
		reads++
		lines, err := cl.taskLog(ctx, t.Node, t.UPID)
		if err != nil {
			return taskInfo{}, false
		}
		ti = parseTaskLog(lines)
		c.mu.Lock()
		c.dev(devID).taskLogs[key] = ti
		c.mu.Unlock()
		return ti, true
	}
}

// logKey identifies a finished task (the status is part of it: a log is only cached for
// the outcome it was read with).
func logKey(t task) string { return t.UPID + "\x00" + t.Status }

// evalBackups returns the failures and warnings that still stand, as guest labels
// ("103 nextcloud") or job labels ("Job auf pve2 (job errors)"). ts is sorted oldest first;
// info returns the parsed log of a task (false: unknown, which never clears anything).
func evalBackups(ts []task, info func(task) (taskInfo, bool), names map[string]string) (failed, warned []string) {
	isFailed, isWarned := map[string]bool{}, map[string]bool{}
	addFailed := func(s string) {
		if !isFailed[s] {
			isFailed[s] = true
			failed = append(failed, s)
		}
	}
	addWarned := func(s string) {
		if !isWarned[s] {
			isWarned[s] = true
			warned = append(warned, s)
		}
	}
	for i, t := range ts {
		if t.Status == "OK" {
			continue
		}
		later := ts[i+1:]
		warn := strings.HasPrefix(t.Status, "WARNINGS")
		ti, known := info(t)
		if !warn && known && len(ti.failed) > 0 {
			for _, id := range ti.failed {
				if !slices.ContainsFunc(later, func(l task) bool { return taskOK(l.Status) && covers(l, id, info) }) {
					addFailed(guestLabel(id, names))
				}
			}
			continue
		}
		// A job-level failure or a warning: replaced by a later successful run of the same job.
		if known && slices.ContainsFunc(later, func(l task) bool {
			return (l.Status == "OK" || !warn && taskOK(l.Status)) && sameJob(t, ti, l, info)
		}) {
			continue
		}
		label := "Job auf " + t.Node + " (" + t.Status + ")"
		if t.ID != "" {
			label = guestLabel(t.ID, names)
		}
		if warn {
			addWarned(label)
		} else {
			addFailed(label)
		}
	}
	warned = slices.DeleteFunc(warned, func(s string) bool { return isFailed[s] })
	return failed, warned
}

// covers reports whether task t backed up guest id.
func covers(t task, id string, info func(task) (taskInfo, bool)) bool {
	ti, ok := info(t)
	return ok && slices.Contains(ti.finished, id)
}

// sameJob reports whether the later task b is a run of the same backup as a (with log info ai).
func sameJob(a task, ai taskInfo, b task, info func(task) (taskInfo, bool)) bool {
	if a.ID != "" {
		return b.ID == a.ID || b.ID == "" && covers(b, a.ID, info)
	}
	if b.ID != "" || b.Node != a.Node {
		return false
	}
	bi, ok := info(b)
	return ok && (ai.job == "" || bi.job == "" || ai.job == bi.job)
}

func taskOK(status string) bool { return status == "OK" || strings.HasPrefix(status, "WARNINGS") }

func guestLabel(id string, names map[string]string) string {
	return strings.TrimSpace(id + " " + names[id])
}
