package proxmox

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"pk95monitoring/internal/model"
)

// Last backup per guest: the backup storages (content "backup", e.g. NFS, PBS) are listed and
// the newest backup of every guest is taken; /cluster/backup-info/not-backed-up names the
// guests no backup job covers. Listing a PBS storage can take a while, so the result is
// cached (guestBackupTTL). Checks "pve:bak:<vmid>" (group Sicherungen) with the metric
// backup.age (hours) – the age thresholds judge it.

const guestBackupTTL = 15 * time.Minute

// backupVol is a backup volume of a storage (/nodes/{node}/storage/{storage}/content).
type backupVol struct {
	Volid        string `json:"volid"`
	VMID         num    `json:"vmid"`
	Ctime        num    `json:"ctime"`
	Size         num    `json:"size"`
	Subtype      string `json:"subtype"`
	Verification *struct {
		State string `json:"state"` // ok, failed
	} `json:"verification"`
}

func (c *client) backupContent(ctx context.Context, node, storage string) ([]backupVol, error) {
	var out []backupVol
	err := c.get(ctx, nodePath(node)+"/storage/"+url.PathEscape(storage)+"/content", url.Values{"content": {"backup"}}, &out)
	return out, err
}

// notBackedUp lists the guests no backup job covers.
func (c *client) notBackedUp(ctx context.Context) ([]resource, error) {
	var out []resource
	err := c.get(ctx, "/cluster/backup-info/not-backed-up", nil, &out)
	return out, err
}

// lastBackup is the newest backup of a guest.
type lastBackup struct {
	at, size float64
	storage  string
	verify   string // ok, failed, "" = not verified
}

type guestBackups struct {
	last     map[string]lastBackup // VMID -> newest backup
	notInJob map[string]bool       // VMIDs without a backup job (nil = unknown)
	errs     []string              // storages that could not be listed
	storages int                   // backup storages listed
	at       time.Time
}

// guestBackupsFor returns the backups of the guests, cached for guestBackupTTL.
func (c *Collector) guestBackupsFor(ctx context.Context, cl *client, devID string, inv inventory, now time.Time) guestBackups {
	c.mu.Lock()
	gb := c.dev(devID).guestBak
	c.mu.Unlock()
	if gb != nil && now.Sub(gb.at) < guestBackupTTL {
		return *gb
	}
	out := guestBackups{last: map[string]lastBackup{}, at: now}
	seen := map[string]bool{}
	for _, s := range inv.storages {
		if !inv.online[s.Node] || !slices.Contains(strings.Split(s.Content, ","), "backup") {
			continue
		}
		key := s.Node + "/" + s.Storage
		if s.Shared == 1 {
			key = s.Storage
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		vols, err := cl.backupContent(ctx, s.Node, s.Storage)
		if err != nil {
			if isStatus(err, http.StatusForbidden) {
				out.errs = append(out.errs, s.Storage+": keine Berechtigung (Datastore.Audit)")
			} else {
				out.errs = append(out.errs, s.Storage+": "+friendly(err).Error())
			}
			continue
		}
		out.storages++
		for _, v := range vols {
			id := strconv.Itoa(int(v.VMID))
			if v.VMID <= 0 {
				continue
			}
			if cur, ok := out.last[id]; ok && cur.at >= float64(v.Ctime) {
				continue
			}
			lb := lastBackup{at: float64(v.Ctime), size: float64(v.Size), storage: s.Storage}
			if v.Verification != nil {
				lb.verify = v.Verification.State
			}
			out.last[id] = lb
		}
	}
	if list, err := cl.notBackedUp(ctx); err == nil {
		out.notInJob = map[string]bool{}
		for _, g := range list {
			out.notInJob[g.vmid()] = true
		}
	}
	c.mu.Lock()
	c.dev(devID).guestBak = &out
	c.mu.Unlock()
	return out
}

// guestBackupChecks builds one check per guest (templates and ignored guests left out).
func guestBackupChecks(inv inventory, gb guestBackups, ignore []string, now time.Time) []model.Check {
	if gb.storages == 0 && len(gb.errs) == 0 && gb.notInJob == nil {
		return nil // no backup storage at all
	}
	var out []model.Check
	for _, g := range inv.guests {
		id := g.vmid()
		if slices.ContainsFunc(ignore, func(s string) bool { return strings.TrimSpace(s) == id }) {
			continue
		}
		kind, label, _, _ := guestKind(g.Type)
		ch := model.Check{Key: "pve:bak:" + id, Name: strings.TrimSpace("Sicherung " + label + " " + id + " " + g.Name), Group: "Sicherungen",
			State: model.StateOK, Facts: map[string]string{"Knoten": g.Node}}
		noJob := gb.notInJob != nil && gb.notInJob[id]
		lb, ok := gb.last[id]
		switch {
		case ok:
			age := max(0, now.Sub(time.Unix(int64(lb.at), 0)).Hours())
			ch.Metrics = []model.Metric{{Name: "backup.age", Instance: kind + "/" + id, Value: round2(age), Unit: "h"},
				{Name: "backup.size", Instance: kind + "/" + id, Value: lb.size, Unit: "B"}}
			ch.Facts["Letzte Sicherung"] = fmtTime(lb.at)
			ch.Facts["Ziel"] = lb.storage
			if lb.size > 0 {
				ch.Facts["Größe"] = fmtBytes(lb.size)
			}
			ch.Message = "Letzte Sicherung " + fmtTime(lb.at) + " (vor " + fmtDuration(age*3600) + ") auf " + lb.storage
			switch lb.verify {
			case "ok":
				ch.Facts["Verifiziert"] = "ja"
			case "failed":
				ch.Facts["Verifiziert"] = "fehlgeschlagen"
				ch.State, ch.Message = model.StateWarn, "Verifizierung der letzten Sicherung fehlgeschlagen – "+ch.Message
			}
			if noJob {
				ch.State, ch.Message = model.StateWarn, "In keinem Sicherungsjob – "+ch.Message
			}
		case noJob:
			ch.State, ch.Message = model.StateWarn, "In keinem Sicherungsjob, keine Sicherung vorhanden"
		case len(gb.errs) > 0:
			ch.State, ch.Message = model.StateUnknown, "Sicherungen nicht lesbar: "+strings.Join(gb.errs, "; ")
		default:
			ch.State, ch.Message = model.StateWarn, "Keine Sicherung gefunden"
		}
		if noJob {
			ch.Facts["Sicherungsjob"] = "keiner"
		}
		out = append(out, ch)
	}
	return out
}
