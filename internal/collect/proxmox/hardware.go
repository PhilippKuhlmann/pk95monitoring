package proxmox

import (
	"strconv"
	"strings"

	"pk95monitoring/internal/model"
)

// wearoutWarn is the used life (percent) of an SSD from which its check warns.
const wearoutWarn = 90

func zfsChecks(node string, pools []zfsPool) []model.Check {
	var checks []model.Check
	for _, p := range pools {
		health := strings.ToUpper(strings.TrimSpace(p.Health))
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
			msg = append(msg, fmtPct(pct(float64(p.Alloc), float64(p.Size)))+" belegt")
		}
		if p.Frag > 0 {
			msg = append(msg, "Fragmentierung "+fmtPct(float64(p.Frag)))
		}
		ch.Message = strings.Join(msg, " · ")
		checks = append(checks, ch)
	}
	return checks
}

func diskChecks(node string, disks []disk) []model.Check {
	var checks []model.Check
	for _, d := range disks {
		dev := strings.TrimPrefix(d.DevPath, "/dev/")
		if dev == "" {
			continue
		}
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
		// PVE reports the remaining life (100 = new) for SSDs and NVMe.
		if d.Wearout.OK && d.Type != "hdd" {
			used := 100 - d.Wearout.V
			ch.Facts["Verschleiß"] = fmtPct(used)
			msg = append(msg, "Verschleiß "+fmtPct(used))
			if used >= wearoutWarn {
				ch.State = model.Worst(ch.State, model.StateWarn)
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
