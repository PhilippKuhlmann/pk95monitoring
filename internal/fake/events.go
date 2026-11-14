package fake

import (
	"strings"
	"time"

	"pk95monitoring/internal/core"
	"pk95monitoring/internal/model"
	"pk95monitoring/internal/winevent"
)

// Demo Windows event log: each Windows agent has a fixed set of events relative to the
// current hour, evaluated with the real rules and exceptions (package winevent), so switching
// rules and ignoring events works in the demo as on a real host.

// demoEvent happens `ago` seconds before the full hour and, with count > 1, every `every`
// seconds before that.
type demoEvent struct {
	log, src   string
	id, level  int
	ago, every int64
	count      int
	msg        string
}

const (
	msgDCOM     = "Die anwendungsspezifischen Berechtigungseinstellungen gewähren keine Berechtigung „Lokal Aktivierung“ für die COM-Serveranwendung mit der CLSID {2593F8B9-4EAF-457C-B68A-50F6B8EA6B54} und der APPID {15C20B67-12E7-4BB6-92BB-7AFF07997402} dem Benutzer NT-AUTORITÄT\\SYSTEM."
	msgSchannel = "Eine schwerwiegende Warnung wurde vom Remoteendpunkt empfangen. Der in der TLS-Protokolldefinition definierte schwerwiegende Warnungscode lautet 46."
	msgLogon    = "Fehler beim Anmelden eines Kontos.\nKontoname: administrator\nFehlerursache: Unbekannter Benutzername oder ungültiges Kennwort.\nQuellnetzwerkadresse: 203.0.113.47"
)

var demoEventLogs = map[string][]demoEvent{
	"dc01": {
		{log: "System", src: "Microsoft-Windows-DistributedCOM", id: 10016, level: 2, ago: 1020, every: 7200, count: 12, msg: msgDCOM},
		{log: "Security", src: "Microsoft-Windows-Security-Auditing", id: 4625, ago: -1500, every: 600, count: 4, msg: msgLogon},
		{log: "Security", src: "Microsoft-Windows-Security-Auditing", id: 4740, ago: 5*3600 + 840,
			msg: "Ein Benutzerkonto wurde gesperrt.\nZu sperrendes Konto: Kontoname: m.weber\nAufrufername des Computers: NB-WEBER"},
	},
	"fs01": {
		{log: "System", src: "Microsoft-Windows-DistributedCOM", id: 10016, level: 2, ago: 2400, every: 10800, count: 8, msg: msgDCOM},
		{log: "System", src: "Schannel", id: 36887, level: 2, ago: 600, every: 3600, count: 20, msg: msgSchannel},
		{log: "System", src: "Service Control Manager", id: 7031, level: 2, ago: 3*3600 + 1500,
			msg: "Der Dienst \"Windows Search\" wurde unerwartet beendet. Dies ist bereits 1 Mal passiert. Folgende Korrekturmaßnahme wird in 30000 Millisekunden durchgeführt: Neustart des Diensts."},
		{log: "Application", src: "Application Error", id: 1000, level: 2, ago: 3*3600 + 1490,
			msg: "Name der fehlerhaften Anwendung: SearchIndexer.exe, Version: 7.0.17763.5830\nName des fehlerhaften Moduls: ntdll.dll"},
	},
}

// events lists the demo events of the agent up to t.
func (a *simAgent) events(t int64) []model.EventEntry {
	hour := t / 3600 * 3600
	var out []model.EventEntry
	for _, d := range demoEventLogs[a.name] {
		for i := range max(1, d.count) {
			ts := hour - d.ago - int64(i)*d.every
			if ts > t {
				ts -= 3600 // not yet this hour
			}
			out = append(out, model.EventEntry{Time: ts, Log: d.log, Source: d.src, ID: d.id, Level: d.level, Message: d.msg})
		}
	}
	return out
}

// eventChecks evaluates the agent's rules over its demo events.
func (a *simAgent) eventChecks(c *core.Core, t int64) []model.Check {
	d, err := c.Device(a.id)
	if err != nil {
		return nil
	}
	rules, ignore := c.AgentEvents(d)
	events := a.events(t)
	res := map[string]winevent.Result{}
	for _, r := range rules {
		list := []model.EventEntry{}
		for _, e := range events {
			if slicesContainsFold(r.Logs, e.Log) {
				list = append(list, e)
			}
		}
		res[r.ID] = winevent.Result{Events: list}
	}
	return winevent.Evaluate(rules, ignore, res, time.Unix(t, 0))
}

func slicesContainsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
