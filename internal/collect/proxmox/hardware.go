package proxmox

import (
	"strconv"
	"strings"
	"time"

	"pk95monitoring/internal/model"
)

func zfsChecks(node string, pools []zfsPool, details map[string]zfsDetail, now time.Time) []model.Check {
	var checks []model.Check
	for _, p := range pools {
		health := strings.ToUpper(strings.TrimSpace(p.Health))
		inst := node + "/" + p.Name
		ch := model.Check{
			Key:   "pve:zfs:" + node + ":" + p.Name,
			Name:  "ZFS " + p.Name + " (" + node + ")",
			Group: "Speicher",
			Facts: map[string]string{"Größe": fmtBytes(float64(p.Size))},
		}
		switch health {
		case "ONLINE":
			ch.State = model.StateOK
		case "DEGRADED":
			ch.State = model.StateWarn
		default:
			ch.State = model.StateCrit
		}
		msg := []string{health}
		if p.Size > 0 {
			u := pct(float64(p.Alloc), float64(p.Size))
			msg = append(msg, fmtPct(u)+" belegt")
			ch.Metrics = append(ch.Metrics, model.Metric{Name: "zfs.usage", Instance: inst, Value: round2(u), Unit: "%"})
			ch.Facts["Frei"] = fmtBytes(float64(p.Free))
		}
		if p.Frag > 0 {
			msg = append(msg, "Fragmentierung "+fmtPct(float64(p.Frag)))
		}
		ch.Metrics = append(ch.Metrics, model.Metric{Name: "zfs.frag", Instance: inst, Value: float64(p.Frag), Unit: "%"})
		ch.Message = strings.Join(msg, " · ")
		if d, ok := details[p.Name]; ok {
			applyZFSDetail(&ch, inst, d, now)
		}
		checks = append(checks, ch)
	}
	return checks
}

// diskChecks reports every physical disk: SMART health from /disks/list and, when PVE
// answers /disks/smart, its attributes (temperature, sector and media errors, spare, wear).
// The thresholds of the disk.* metrics decide warn/crit; the native state covers SMART
// FAILED, attributes failing now and NVMe critical warnings.
func diskChecks(node string, disks []disk, smart map[string]smartEntry) []model.Check {
	var checks []model.Check
	for _, d := range disks {
		dev := strings.TrimPrefix(d.DevPath, "/dev/")
		if dev == "" {
			continue
		}
		inst := node + "/" + dev
		health := strings.ToUpper(strings.TrimSpace(d.Health))
		ch := model.Check{
			Key:   "pve:disk:" + node + ":" + dev,
			Name:  "Platte " + dev + " (" + node + ")",
			Group: "Platten",
			Facts: map[string]string{},
		}
		for k, v := range map[string]string{"Modell": d.Model, "Seriennummer": d.Serial, "Typ": strings.ToUpper(d.Type)} {
			if strings.TrimSpace(v) != "" {
				ch.Facts[k] = strings.TrimSpace(v)
			}
		}
		if d.Size > 0 {
			ch.Facts["Größe"] = fmtBytes(float64(d.Size))
		}
		var msg []string
		switch health {
		case "PASSED", "OK":
			ch.State = model.StateOK
			msg = append(msg, "SMART "+health)
		case "FAILED":
			ch.State = model.StateCrit
			msg = append(msg, "SMART FAILED – Platte tauschen")
		case "", "UNKNOWN":
			ch.State = model.StateOK
			msg = append(msg, "SMART nicht verfügbar")
		default:
			ch.State = model.StateWarn
			msg = append(msg, "SMART "+health)
		}

		sv := newSmartValues()
		if e, ok := smart[d.DevPath]; ok && e.err == nil {
			if len(e.info.Attributes) > 0 {
				sv = ataSmart(e.info.Attributes)
			} else if e.info.Text != "" {
				sv = nvmeSmart(e.info.Text)
			}
		}
		// PVE reports the remaining life (100 = new) for SSDs and NVMe; the metric is the used part.
		if d.Wearout.OK && d.Type != "hdd" {
			sv.v["disk.wearout"] = 100 - d.Wearout.V
		}
		if len(sv.failing) > 0 {
			ch.State = model.StateCrit
			msg = append(msg, "SMART-Wert unter Grenze: "+strings.Join(sv.failing, ", "))
		}
		if len(sv.critMsg) > 0 {
			ch.State = model.StateCrit
			msg = append(msg, "NVMe-Warnung: "+strings.Join(sv.critMsg, ", "))
		}
		if t, ok := sv.v["disk.temp"]; ok {
			msg = append(msg, strconv.Itoa(int(t))+" °C")
		}
		if w, ok := sv.v["disk.wearout"]; ok {
			ch.Facts["Verschleiß"] = fmtPct(w)
			msg = append(msg, "Verschleiß "+fmtPct(w))
		}
		if sp, ok := sv.v["disk.spare"]; ok {
			ch.Facts["Reserve"] = fmtPct(sp)
		}
		msg = append(msg, smartProblems(sv.v)...)
		if n := sv.v["disk.crc_errors"]; n > 0 {
			ch.Facts["CRC-Fehler (Kabel)"] = groupThousands(int64(n))
		}
		for k, v := range sv.facts {
			ch.Facts[k] = v
		}
		for _, name := range []string{"disk.temp", "disk.wearout", "disk.spare", "disk.reallocated", "disk.pending", "disk.uncorrectable", "disk.media_errors", "disk.crc_errors"} {
			if v, ok := sv.v[name]; ok {
				m := model.Metric{Name: name, Instance: inst, Value: round2(v)}
				switch name {
				case "disk.temp":
					m.Unit = "°C"
				case "disk.wearout", "disk.spare":
					m.Unit = "%"
				}
				ch.Metrics = append(ch.Metrics, m)
			}
		}
		if d.Model != "" {
			msg = append(msg, strings.TrimSpace(d.Model))
		}
		ch.Message = strings.Join(msg, " · ")
		checks = append(checks, ch)
	}
	return checks
}

func updatesCheck(node string, e aptEntry) model.Check {
	ch := model.Check{Key: "pve:updates:" + node, Name: "Updates " + node, Group: "Updates"}
	if e.err != nil {
		ch.State, ch.Message = model.StateUnknown, permMessage(e.err, "Sys.Modify auf /nodes")
		return ch
	}
	ch.State = model.StateOK
	ch.Facts = map[string]string{"Stand": e.at.Format("02.01.2006 15:04")}
	ch.Metrics = []model.Metric{
		{Name: "updates.pending", Instance: node, Value: float64(e.pending)},
		{Name: "updates.security", Instance: node, Value: float64(e.security)},
	}
	switch {
	case e.pending == 0:
		ch.Message = "System aktuell"
	case e.pending == 1:
		ch.Message = "1 Update verfügbar"
	default:
		ch.Message = strconv.Itoa(e.pending) + " Updates verfügbar"
	}
	if e.security > 0 {
		ch.Message += ", davon " + strconv.Itoa(e.security) + " Sicherheitsupdate"
		if e.security > 1 {
			ch.Message += "s"
		}
	}
	return ch
}
