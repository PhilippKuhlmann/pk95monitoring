package agent

import (
	"fmt"
	"strings"

	"pk95monitoring/internal/model"
)

// buildUpdatesCheck turns an update count into the "updates" check. A pending reboot is a
// warning; the security count is judged by the server (threshold updates.security).
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
	}
	if reboot {
		ch.State = model.Worst(ch.State, model.StateWarn)
		ch.Message = "Neustart erforderlich – " + ch.Message
	}
	return ch
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

// eventlogCheck turns the Windows event log counts into the "eventlog" check: critical
// events in the last hour are a warning, the 24 h error count is a metric for thresholds.
func eventlogCheck(r eventlogResult, err error) model.Check {
	ch := model.Check{Key: "eventlog", Name: "Ereignisprotokoll", Group: groupSystem, State: model.StateOK}
	if err != nil {
		ch.State, ch.Message = model.StateUnknown, truncate(err.Error(), 200)
		return ch
	}
	ch.Metrics = []model.Metric{{Name: "eventlog.errors", Value: float64(r.Errors)}}
	ch.Message = fmt.Sprintf("%d Fehler in 24 Std. (System und Anwendung)", r.Errors)
	if r.Critical > 0 {
		ch.State = model.StateWarn
		ch.Message = fmt.Sprintf("%d kritische Ereignisse in der letzten Stunde", r.Critical)
		if r.Last != "" {
			ch.Message += ", zuletzt " + truncate(strings.TrimSpace(r.Last), 200)
		}
	}
	return ch
}
