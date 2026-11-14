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

// BackupOverview is the page "Sicherungen".
type BackupOverview struct {
	Rows     []BackupRow     `json:"rows"`
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
	ov := BackupOverview{Rows: []BackupRow{}, Storages: []BackupStorage{}, Servers: []BackupServer{}}
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
	slices.SortFunc(ov.Servers, func(a, b BackupServer) int { return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	slices.SortFunc(ov.Storages, func(a, b BackupStorage) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Device), strings.ToLower(b.Device)), cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)))
	})
	return ov
}
