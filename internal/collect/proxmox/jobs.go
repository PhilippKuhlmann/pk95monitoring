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

const (
	// jobHistoryWindow is how far back vzdump tasks are read for the runs of the backup jobs
	// (a weekly job still shows five runs).
	jobHistoryWindow = 35 * 24 * time.Hour
	// jobsTTL is how often the job list and the task history are read again.
	jobsTTL = 10 * time.Minute
	// runGap: tasks of a job starting within this time of a run's first task belong to that
	// run (an "all" job starts one task per node).
	runGap = 30 * time.Minute
	// maxJobLogs bounds the task logs read per poll for the job history (newest first).
	maxJobLogs = 20
)

// backupJobConf is an entry of /cluster/backup.
type backupJobConf struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Enabled  *num   `json:"enabled"` // missing = enabled
	Schedule string `json:"schedule"`
	Start    string `json:"starttime"` // jobs from before PVE 7.1: starttime + dow
	Dow      string `json:"dow"`
	Storage  string `json:"storage"`
	Comment  string `json:"comment"`
	VMID     text   `json:"vmid"`
	All      num    `json:"all"`
	Pool     string `json:"pool"`
	Exclude  text   `json:"exclude"`
	NextRun  num    `json:"next-run"`
}

func (j backupJobConf) enabled() bool { return j.Enabled == nil || *j.Enabled != 0 }

// jobHistory is the cached job list and vzdump tasks of a cluster.
type jobHistory struct {
	at    time.Time
	jobs  []backupJobConf
	tasks []task // finished vzdump tasks of jobHistoryWindow, all nodes
	err   error
}

// jobLog is what the log of a finished vzdump task says about its job.
type jobLog struct {
	cmd    vzdumpCmd
	failed []string
}

func (c *client) backupJobs(ctx context.Context) ([]backupJobConf, error) {
	var out []backupJobConf
	err := c.get(ctx, "/cluster/backup", nil, &out)
	return out, err
}

// backupJobs returns the backup jobs of the cluster with their latest runs. The job list
// and the tasks are read every jobsTTL; task logs (which tell the job of a task) are read
// newest first, at most maxJobLogs per poll, and kept while the task is in the window.
// Without permission for /cluster/backup (Sys.Audit on /) the list stays empty.
func (c *Collector) backupJobs(ctx context.Context, cl *client, devID string, inv inventory, now time.Time) []model.BackupJob {
	c.mu.Lock()
	h := c.dev(devID).jobs
	c.mu.Unlock()
	if h == nil || now.Sub(h.at) >= jobsTTL {
		h = &jobHistory{at: now}
		h.jobs, h.err = cl.backupJobs(ctx)
		if h.err == nil {
			since := now.Add(-jobHistoryWindow).Unix()
			for _, n := range inv.nodes {
				if !inv.online[n.Node] {
					continue
				}
				if ts, err := cl.backupTasks(ctx, n.Node, since); err == nil {
					h.tasks = append(h.tasks, ts...)
				}
			}
		}
		c.mu.Lock()
		c.dev(devID).jobs = h
		c.mu.Unlock()
	}
	if h.err != nil {
		return nil
	}
	var confs []backupJobConf
	for _, j := range h.jobs {
		if j.Type == "" || j.Type == "vzdump" {
			confs = append(confs, j)
		}
	}
	if len(confs) == 0 {
		return nil
	}

	var ts []task
	for _, t := range h.tasks {
		if t.EndTime != 0 && t.Status != "" {
			ts = append(ts, t)
		}
	}
	slices.SortFunc(ts, func(a, b task) int { return cmp.Compare(b.StartTime, a.StartTime) }) // newest first
	logs := c.jobLogs(ctx, cl, devID, ts)

	byJob := map[string][]task{}
	for _, t := range ts {
		l, ok := logs[t.UPID]
		if !ok {
			continue
		}
		if id := matchJob(l.cmd, confs); id != "" {
			byJob[id] = append(byJob[id], t)
		}
	}
	out := make([]model.BackupJob, 0, len(confs))
	for _, j := range confs {
		bj := model.BackupJob{ID: j.ID, Name: strings.TrimSpace(j.Comment), Schedule: j.schedule(), Target: j.Storage,
			Selection: j.selection(inv.names), Enabled: j.enabled(), Next: int64(j.NextRun)}
		if bj.Name == "" {
			bj.Name = bj.Selection
		}
		bj.Runs = jobRuns(byJob[j.ID], logs, inv.names)
		out = append(out, bj)
	}
	slices.SortFunc(out, func(a, b model.BackupJob) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), strings.Compare(a.ID, b.ID))
	})
	return out
}

// jobLogs returns the parsed logs of the tasks ts (newest first) that are known, reading
// at most maxJobLogs missing ones. The cache keeps only tasks of ts.
func (c *Collector) jobLogs(ctx context.Context, cl *client, devID string, ts []task) map[string]jobLog {
	c.mu.Lock()
	cache := c.dev(devID).jobLogs
	keep := make(map[string]bool, len(ts))
	for _, t := range ts {
		keep[t.UPID] = true
	}
	for k := range cache {
		if !keep[k] {
			delete(cache, k)
		}
	}
	out := make(map[string]jobLog, len(cache))
	for k, v := range cache {
		out[k] = v
	}
	c.mu.Unlock()

	reads := 0
	for _, t := range ts {
		if _, ok := out[t.UPID]; ok {
			continue
		}
		if reads >= maxJobLogs {
			break
		}
		reads++
		lines, err := cl.taskLog(ctx, t.Node, t.UPID)
		if err != nil {
			continue
		}
		ti := parseTaskLog(lines)
		l := jobLog{cmd: parseVzdump(ti.job), failed: ti.failed}
		out[t.UPID] = l
		c.mu.Lock()
		c.dev(devID).jobLogs[t.UPID] = l
		c.mu.Unlock()
	}
	return out
}

// jobRuns groups the tasks of one job into runs: tasks starting within runGap of a run's
// first task are one run (one task per node). The newest MaxJobRuns, oldest first.
func jobRuns(ts []task, logs map[string]jobLog, names map[string]string) []model.JobRun {
	sorted := slices.Clone(ts)
	slices.SortFunc(sorted, func(a, b task) int { return cmp.Compare(a.StartTime, b.StartTime) })
	type acc struct {
		run      model.JobRun
		failed   []string
		statuses []string
	}
	var runs []*acc
	for _, t := range sorted {
		var cur *acc
		if n := len(runs); n > 0 && int64(t.StartTime)-runs[n-1].run.Start < int64(runGap.Seconds()) {
			cur = runs[n-1]
		} else {
			cur = &acc{run: model.JobRun{Start: int64(t.StartTime), State: model.StateOK}}
			runs = append(runs, cur)
		}
		cur.run.End = max(cur.run.End, int64(t.EndTime))
		st := model.StateOK
		switch {
		case t.Status == "OK":
		case strings.HasPrefix(t.Status, "WARNINGS"):
			st = model.StateWarn
		default:
			st = model.StateCrit
			failed := logs[t.UPID].failed
			if len(failed) == 0 && t.ID != "" {
				failed = []string{t.ID}
			}
			for _, id := range failed {
				if l := guestLabel(id, names); !slices.Contains(cur.failed, l) {
					cur.failed = append(cur.failed, l)
				}
			}
			if len(failed) == 0 && !slices.Contains(cur.statuses, t.Status) {
				cur.statuses = append(cur.statuses, t.Status)
			}
		}
		cur.run.State = model.Worst(cur.run.State, st)
	}
	if len(runs) > model.MaxJobRuns {
		runs = runs[len(runs)-model.MaxJobRuns:]
	}
	out := make([]model.JobRun, len(runs))
	for i, a := range runs {
		r := a.run
		switch r.State {
		case model.StateOK:
			r.Message = "Erfolgreich"
		case model.StateWarn:
			r.Message = "Mit Warnungen"
		default:
			r.Message = "Fehlgeschlagen"
			if len(a.failed) > 0 {
				r.Message += ": " + strings.Join(a.failed, ", ")
			} else if len(a.statuses) > 0 {
				r.Message += " (" + strings.Join(a.statuses, ", ") + ")"
			}
		}
		out[i] = r
	}
	return out
}

func (j backupJobConf) schedule() string {
	if s := strings.TrimSpace(j.Schedule); s != "" {
		return s
	}
	return strings.TrimSpace(strings.TrimSpace(j.Dow) + " " + strings.TrimSpace(j.Start))
}

// selection describes the guests of a job in German: "Alle", "Alle außer 106", "Pool prod",
// "100 dc01, 101 web".
func (j backupJobConf) selection(names map[string]string) string {
	switch {
	case j.All != 0:
		if ex := splitIDs(string(j.Exclude)); len(ex) > 0 {
			return "Alle außer " + strings.Join(ex, ", ")
		}
		return "Alle"
	case j.Pool != "":
		return "Pool " + j.Pool
	}
	ids := splitIDs(string(j.VMID))
	labels := make([]string, len(ids))
	for i, id := range ids {
		labels[i] = guestLabel(id, names)
	}
	return strings.Join(labels, ", ")
}

// splitIDs splits a VMID list ("100,101 105") and sorts it numerically without duplicates.
func splitIDs(s string) []string {
	f := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' })
	slices.SortFunc(f, func(a, b string) int {
		x, _ := strconv.Atoi(a)
		y, _ := strconv.Atoi(b)
		return cmp.Or(cmp.Compare(x, y), strings.Compare(a, b))
	})
	return slices.Compact(f)
}

// vzdumpCmd is the command line of a vzdump task ("starting new backup job: vzdump …").
type vzdumpCmd struct {
	known   bool
	jobID   string
	vmids   []string
	all     bool
	pool    string
	exclude []string
	storage string
	manual  bool // "--remove 0": "Backup now" of a guest, not a job
}

func parseVzdump(cmd string) vzdumpCmd {
	f := strings.Fields(cmd)
	if len(f) == 0 || f[0] != "vzdump" {
		return vzdumpCmd{}
	}
	v := vzdumpCmd{known: true}
	for i := 1; i < len(f); i++ {
		a := f[i]
		if !strings.HasPrefix(a, "--") {
			if _, err := strconv.Atoi(a); err == nil {
				v.vmids = append(v.vmids, a)
			}
			continue
		}
		name, val, ok := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		if !ok && i+1 < len(f) && !strings.HasPrefix(f[i+1], "--") {
			i++
			val = f[i]
		}
		val = strings.Trim(val, `'"`)
		switch name {
		case "job-id":
			v.jobID = val
		case "all":
			v.all = val != "0"
		case "pool":
			v.pool = val
		case "exclude":
			v.exclude = splitIDs(val)
		case "storage":
			v.storage = val
		case "remove":
			v.manual = val == "0"
		}
	}
	return v
}

// matchJob returns the ID of the job a task ran for: the job ID PVE 8.2 and later log, else
// the job with the same storage and selection (a job with guests on several nodes starts one
// task per node with that node's guests). Manual backups ("Backup now") match no job.
func matchJob(v vzdumpCmd, jobs []backupJobConf) string {
	if !v.known {
		return ""
	}
	if v.jobID != "" {
		for _, j := range jobs {
			if j.ID == v.jobID {
				return j.ID
			}
		}
		return ""
	}
	if v.manual {
		return ""
	}
	for _, j := range jobs {
		if j.Storage != v.storage {
			continue
		}
		switch {
		case j.All != 0:
			if v.all && slices.Equal(splitIDs(string(j.Exclude)), v.exclude) {
				return j.ID
			}
		case j.Pool != "":
			if v.pool == j.Pool {
				return j.ID
			}
		default:
			ids := splitIDs(string(j.VMID))
			if !v.all && v.pool == "" && len(v.vmids) > 0 && !slices.ContainsFunc(v.vmids, func(id string) bool { return !slices.Contains(ids, id) }) {
				return j.ID
			}
		}
	}
	return ""
}
