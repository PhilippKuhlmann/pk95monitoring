package agent

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"pk95monitoring/internal/model"
)

// buildUpdatesCheck turns an update count into the "updates" check. A pending reboot is a
// warning; the counts and the age of the last installation are judged by the server
// (thresholds updates.security, updates.age, updates.failed).
func buildUpdatesCheck(c updateCount, err error, reboot bool) model.Check {
	ch := model.Check{Key: "updates", Name: "Updates", Group: groupUpdates, State: model.StateOK}
	if err != nil {
		ch.State, ch.Message = model.StateUnknown, "Updatesuche fehlgeschlagen: "+truncate(err.Error(), 200)
	} else {
		ch.Metrics = []model.Metric{
			{Name: "updates.pending", Value: float64(c.Pending)},
			{Name: "updates.security", Value: float64(c.Security)},
		}
		ch.Message = updatesMessage(c)
		addUpdateHistory(&ch, c, time.Now())
	}
	if reboot {
		ch.State = model.Worst(ch.State, model.StateWarn)
		ch.Message = "Neustart erforderlich – " + ch.Message
	}
	return ch
}

// addUpdateHistory adds the Windows update history (last installation, failed updates,
// pending titles) as metrics and facts.
func addUpdateHistory(ch *model.Check, c updateCount, now time.Time) {
	facts := map[string]string{}
	if c.LastInstall > 0 {
		last := time.Unix(c.LastInstall, 0)
		days := max(0, math.Floor(now.Sub(last).Hours()/24))
		ch.Metrics = append(ch.Metrics, model.Metric{Name: "updates.age", Value: days, Unit: "d"})
		facts["Letzte Installation"] = last.Local().Format("02.01.2006 15:04")
		if days >= 1 {
			ch.Message += fmt.Sprintf(" · letzte Installation vor %s %s", strconv.Itoa(int(days)), plural(int(days), "Tag", "Tagen"))
		}
	}
	if c.LastInstall > 0 || c.Failed > 0 || c.Titles != nil {
		ch.Metrics = append(ch.Metrics, model.Metric{Name: "updates.failed", Value: float64(c.Failed)})
	}
	if c.Failed > 0 {
		facts["Fehlgeschlagen (30 Tage)"] = strconv.Itoa(c.Failed)
		ch.Message += fmt.Sprintf(" · %d fehlgeschlagen", c.Failed)
	}
	for i, t := range c.Titles {
		if i == 5 {
			facts["Ausstehend"] += fmt.Sprintf(" · und %d weitere", c.Pending-5)
			break
		}
		if i > 0 {
			facts["Ausstehend"] += " · "
		}
		facts["Ausstehend"] += truncate(strings.TrimSpace(t), 120)
	}
	if len(facts) > 0 {
		ch.Facts = facts
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func updatesMessage(c updateCount) string {
	switch {
	case c.Pending == 0:
		return "System ist aktuell"
	case c.Pending == 1 && c.Security == 1:
		return "1 Update (Sicherheitsupdate)"
	case c.Pending == 1:
		return "1 Update verfügbar"
	case c.Security == 0:
		return fmt.Sprintf("%d Updates verfügbar", c.Pending)
	case c.Security == 1:
		return fmt.Sprintf("%d Updates, davon 1 Sicherheitsupdate", c.Pending)
	}
	return fmt.Sprintf("%d Updates, davon %d Sicherheitsupdates", c.Pending, c.Security)
}
