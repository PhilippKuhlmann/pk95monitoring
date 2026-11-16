package core

import (
	"cmp"
	"slices"
	"strings"

	"pk95monitoring/internal/model"
)

// BackupRow is one backup on the page "Sicherungen": the last backup of a Proxmox guest
// ("pve:bak:<vmid>"), the vzdump tasks of a cluster ("pve:backup"), a Veeam job
// ("veeam:job:<id>"), a backup group, job or verification of a Proxmox Backup Server
// ("pbs:group:…", "pbs:task:…", "pbs:verify:…") or a system log rule about backups
// ("ev:backup", "ev:veeam").
type BackupRow struct {
	DeviceID string   `json:"device_id"`
	Device   string   `json:"device"`
	Group    string   `json:"group,omitempty"` // folder of the device
	Type     string   `json:"type"`            // device type
	CheckKey string   `json:"check_key"`
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`           // guest, tasks, job, group, pbsjob, verify, log
	Age      *float64 `json:"age"`            // hours since the last backup or run; nil = none
	Last     int64    `json:"last,omitempty"` // time of the last backup or run
	Size     *float64 `json:"size"`
	Target   string   `json:"target,omitempty"`   // storage or repository
	Next     string   `json:"next,omitempty"`     // next run (Veeam)
	Result   string   `json:"result,omitempty"`   // job result, verification
	JobType  string   `json:"job_type,omitempty"` // Veeam job type, PBS task type
	State    string   `json:"state"`
	Acked    bool     `json:"acked,omitempty"`
	Mode     string   `json:"mode,omitempty"`
	Message  string   `json:"message"`
}

// BackupStorage is a backup target with its fill level (Veeam repositories, PBS datastores).
type BackupStorage struct {
	DeviceID string   `json:"device_id"`
	Device   string   `json:"device"`
	CheckKey string   `json:"check_key"`
	Name     string   `json:"name"`
	Usage    *float64 `json:"usage"` // percent; nil = unknown
	Used     *float64 `json:"used"`
	Total    *float64 `json:"total"`
	State    string   `json:"state"`
	Message  string   `json:"message"`
}

// BackupServer is a Veeam server or Proxmox Backup Server (model.BackupServer): they are
// managed on the page "Sicherungen", not in the device list.
type BackupServer struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Host     string `json:"host"`
	Group    string `json:"group,omitempty"`
	Type     string `json:"type"`
	Enabled  bool   `json:"enabled"`
	State    string `json:"state"` // reachability
	Message  string `json:"message"`
	Version  string `json:"version,omitempty"`
	LastPoll int64  `json:"last_poll"`
}

// BackupJob is a backup job on the page "Sicherungen" with its latest runs: a backup job of
// a Proxmox VE cluster (reported with the check "pve:backup", which also opens on click), a
// Veeam job ("veeam:job:<id>") or a job of a Proxmox Backup Server ("pbs:task:<type>:<id>").
// The state of a Proxmox job is that of its last run (it raises no alert of its own: failed
// tasks are the check "pve:backup").
type BackupJob struct {
	DeviceID  string         `json:"device_id"`
	Device    string         `json:"device"`
	Type      string         `json:"type"` // device type
	CheckKey  string         `json:"check_key"`
	ID        string         `json:"id,omitempty"` // Proxmox job ID
	Name      string         `json:"name"`
	JobType   string         `json:"job_type,omitempty"`  // Veeam job type, PBS task type
	Selection string         `json:"selection,omitempty"` // Proxmox: guests of the job
	Schedule  string         `json:"schedule,omitempty"`  // Proxmox
	Target    string         `json:"target,omitempty"`
	Enabled   bool           `json:"enabled"`
	Last      int64          `json:"last,omitempty"`    // end (else start) of the last run
	Next      string         `json:"next,omitempty"`    // next run as text (Veeam)
	NextTS    int64          `json:"next_ts,omitempty"` // next run (Proxmox)
	State     string         `json:"state"`
	Acked     bool           `json:"acked,omitempty"`
	Mode      string         `json:"mode,omitempty"`
	Message   string         `json:"message"`
	Runs      []model.JobRun `json:"runs"` // oldest first, at most model.MaxJobRuns
}

// BackupOverview is the page "Sicherungen".
type BackupOverview struct {
	Rows     []BackupRow     `json:"rows"`
	Jobs     []BackupJob     `json:"jobs"`
	Storages []BackupStorage `json:"storages"`
	Servers  []BackupServer  `json:"servers"`
}

func backupKind(key string) string {
	switch {
	case strings.HasPrefix(key, "pve:bak:"):
		return "guest"
	case key == "pve:backup":
		return "tasks"
	case strings.HasPrefix(key, "veeam:job:"):
		return "job"
	case strings.HasPrefix(key, "pbs:group:"):
		return "group"
	case strings.HasPrefix(key, "pbs:task:"):
		return "pbsjob"
	case strings.HasPrefix(key, "pbs:verify:"):
		return "verify"
	case key == "ev:backup" || key == "ev:veeam":
		return "log"
	}
	return ""
}

func metricOf(cs model.CheckStatus, name string) *float64 {
	for _, m := range cs.Metrics {
		if m.Name == name {
			v := m.Value
			return &v
		}
	}
	return nil
}

// Backups lists the backups of all enabled devices, worst first.
func (c *Core) Backups() BackupOverview {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.Now().Unix()
	ov := BackupOverview{Rows: []BackupRow{}, Jobs: []BackupJob{}, Storages: []BackupStorage{}, Servers: []BackupServer{}}
	for id, d := range c.devices {
		rt := c.rt[id]
		if model.BackupServer(d.Type) {
			srv := BackupServer{ID: id, Name: d.Name, Host: d.Host, Group: d.Group, Type: d.Type, Enabled: d.Enabled, State: model.StateUnknown}
			if rt != nil {
				srv.LastPoll = rt.st.LastPoll
				for _, cs := range rt.st.Checks {
					switch cs.Key {
					case reachKey:
						srv.State, srv.Message = cs.State, cs.Message
					case "veeam:server", "pbs:server":
						srv.Version = cs.Facts["Version"]
					}
				}
			}
			if !d.Enabled {
				srv.State, srv.Message = model.StateUnknown, "Deaktiviert"
			}
			ov.Servers = append(ov.Servers, srv)
		}
		if rt == nil || !d.Enabled {
			continue
		}
		name, group := c.displayName(d), d.Group
		if p, ok := c.parentOf(d); ok && group == "" {
			group = p.Group
		}
		for _, cs := range rt.st.Checks {
			if strings.HasPrefix(cs.Key, "veeam:repo:") || strings.HasPrefix(cs.Key, "pbs:ds:") {
				sname := strings.TrimPrefix(strings.TrimPrefix(cs.Name, "Repository "), "Datastore ")
				ov.Storages = append(ov.Storages, BackupStorage{DeviceID: id, Device: name, CheckKey: cs.Key, Name: sname,
					Usage: metricOf(cs, "storage.usage"), Used: metricOf(cs, "storage.used"), Total: metricOf(cs, "storage.total"), State: cs.State, Message: cs.Message})
				continue
			}
			kind := backupKind(cs.Key)
			if kind == "" {
				continue
			}
			ov.Jobs = append(ov.Jobs, jobsOf(id, name, d.Type, kind, cs, rt.st.LastPoll)...)
			r := BackupRow{DeviceID: id, Device: name, Group: group, Type: d.Type, CheckKey: cs.Key, Name: cs.Name, Kind: kind,
				State: cs.State, Acked: cs.Acked, Mode: cs.Mode, Message: cs.Message, Size: metricOf(cs, "backup.size"),
				Target: cs.Facts["Ziel"], Next: cs.Facts["Nächster Lauf"], JobType: cs.Facts["Typ"]}
			if cs.Reason != "" && cs.Reason != cs.Message && (cs.State == model.StateWarn || cs.State == model.StateCrit) {
				r.Message = cs.Reason + " – " + cs.Message // a threshold decided (age of the backup)
			}
			for _, m := range []string{"backup.age", "group.age", "job.age"} {
				if r.Age == nil {
					r.Age = metricOf(cs, m)
				}
			}
			if r.Age != nil {
				// the age was measured at the last poll
				r.Last = rt.st.LastPoll - int64(*r.Age*3600)
				if r.Last > now {
					r.Last = now
				}
			}
			switch {
			case cs.Facts["Ergebnis"] != "":
				r.Result = cs.Facts["Ergebnis"]
			case cs.Facts["Verifiziert"] != "":
				r.Result = "Verifiziert: " + cs.Facts["Verifiziert"]
			}
			ov.Rows = append(ov.Rows, r)
		}
	}
	rank := func(state string, acked bool, mode string) int {
		if acked || mode != "" {
			return 0
		}
		return model.Rank(state)
	}
	slices.SortFunc(ov.Rows, func(a, b BackupRow) int {
		return cmp.Or(rank(b.State, b.Acked, b.Mode)-rank(a.State, a.Acked, a.Mode),
			cmp.Compare(strings.ToLower(a.Device), strings.ToLower(b.Device)), strings.Compare(a.Kind, b.Kind),
			cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)))
	})
	slices.SortFunc(ov.Jobs, func(a, b BackupJob) int {
		return cmp.Or(rank(b.State, b.Acked, b.Mode)-rank(a.State, a.Acked, a.Mode),
			cmp.Compare(strings.ToLower(a.Device), strings.ToLower(b.Device)), cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)))
	})
	slices.SortFunc(ov.Servers, func(a, b BackupServer) int { return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	slices.SortFunc(ov.Storages, func(a, b BackupStorage) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Device), strings.ToLower(b.Device)), cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)))
	})
	return ov
}

// jobsOf returns the backup jobs a check stands for: the Proxmox jobs reported with
// "pve:backup", or the Veeam or PBS job the check is.
func jobsOf(devID, device, typ, kind string, cs model.CheckStatus, lastPoll int64) []BackupJob {
	switch kind {
	case "tasks":
		out := make([]BackupJob, 0, len(cs.Jobs))
		for _, j := range cs.Jobs {
			bj := BackupJob{DeviceID: devID, Device: device, Type: typ, CheckKey: cs.Key, ID: j.ID, Name: j.Name,
				Selection: j.Selection, Schedule: j.Schedule, Target: j.Target, Enabled: j.Enabled, NextTS: j.Next, Runs: j.Runs}
			if bj.Runs == nil {
				bj.Runs = []model.JobRun{}
			}
			switch {
			case !j.Enabled:
				bj.State, bj.Message = model.StateUnknown, "Deaktiviert"
			case len(j.Runs) == 0:
				bj.State, bj.Message = model.StateUnknown, "Kein Lauf in den letzten 35 Tagen"
			default:
				last := j.Runs[len(j.Runs)-1]
				bj.State, bj.Message = last.State, last.Message
			}
			if n := len(j.Runs); n > 0 {
				bj.Last = cmp.Or(j.Runs[n-1].End, j.Runs[n-1].Start)
			}
			out = append(out, bj)
		}
		return out
	case "job", "pbsjob":
		bj := BackupJob{DeviceID: devID, Device: device, Type: typ, CheckKey: cs.Key, Name: cs.Name, JobType: cs.Facts["Typ"],
			Target: cs.Facts["Ziel"], Next: cs.Facts["Nächster Lauf"], Enabled: !strings.HasPrefix(cs.Message, "Deaktiviert"),
			State: cs.State, Acked: cs.Acked, Mode: cs.Mode, Message: cs.Message, Runs: cs.Runs}
		if bj.Runs == nil {
			bj.Runs = []model.JobRun{}
		}
		if n := len(cs.Runs); n > 0 {
			last := cs.Runs[n-1]
			bj.Last = cmp.Or(last.End, last.Start)
			// the last run's result says more than the check message (which repeats the times
			// shown in their own columns), unless the check says something else
			if last.State == cs.Native && !strings.HasPrefix(cs.Message, "Geplanter Lauf") {
				bj.Message = last.Message
			}
		} else if age := metricOf(cs, "job.age"); age != nil && lastPoll > 0 {
			bj.Last = lastPoll - int64(*age*3600)
		}
		return []BackupJob{bj}
	}
	return nil
}
