package core

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"pk95monitoring/internal/model"
	"pk95monitoring/internal/notify"
	"pk95monitoring/internal/store"
)

// sent is one delivered notification with its channels.
type sent struct {
	title    string
	mail     string
	telegram bool
	m        notify.Message
}

type sink struct {
	mu   sync.Mutex
	list []sent
}

func (s *sink) send(_ context.Context, ns notify.Settings, m notify.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	mail := ""
	if ns.MailEnabled {
		mail = ns.MailTo
	}
	s.list = append(s.list, sent{title: m.Title, mail: mail, telegram: ns.TelegramEnabled, m: m})
	return nil
}

func (s *sink) take() []sent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.list
	s.list = nil
	return out
}

// notifyEnv has mail (default recipient admin@example.org) and Telegram switched on.
func notifyEnv(t *testing.T) (*env, *sink) {
	e := newEnv(t, Options{})
	s := &sink{}
	e.c.Notify = s.send
	n := &e.c.settings.Notify
	n.MailEnabled, n.SMTPHost, n.MailFrom, n.MailTo = true, "smtp.example.org", "mon@example.org", "admin@example.org"
	n.TelegramEnabled, n.TelegramToken, n.TelegramChat = true, "123:abc", "42"
	n.HookEnabled = false
	return e, s
}

func svcState(state string) model.Check {
	return model.Check{Key: "svc:app", Name: "App", Group: "Dienste", State: state, Message: "Dienst " + state}
}

func (e *env) at(t time.Time) { e.clk.mu.Lock(); e.clk.t = t; e.clk.mu.Unlock() }

func TestNotifyRulesRouting(t *testing.T) {
	e, s := notifyEnv(t)
	srv := e.agentDevice(t, "srv", func(d *model.Device) { d.Group = "Server/Nord" })
	pc := e.agentDevice(t, "pc", nil)
	start := e.clk.Now() // Wednesday 10:00

	// without rules: every alert to all channels (as before)
	e.report(t, pc, svcState(model.StateWarn))
	e.wait()
	if l := s.take(); len(l) != 1 || l[0].mail != "admin@example.org" || !l[0].telegram {
		t.Fatalf("without rules: %+v", l)
	}

	a, errs, err := e.c.SaveNotifyRule(NotifyRule{Name: "Server", Enabled: true, Folders: []string{"Server"}, MinSeverity: model.StateWarn,
		Mail: true, MailTo: "it@example.org", RepeatMinutes: 60, EscalateMinutes: 30, EscalateMailTo: "chef@example.org"})
	if err != nil || len(errs) > 0 || a.Matches != 1 {
		t.Fatalf("rule a: %+v %v %v", a, errs, err)
	}
	if _, errs, err := e.c.SaveNotifyRule(NotifyRule{Name: "Kritisch", Enabled: true, MinSeverity: model.StateCrit, Telegram: true,
		Kinds: []string{NotifyReach, NotifyChecks}}); err != nil || len(errs) > 0 {
		t.Fatalf("rule b: %v %v", errs, err)
	}
	if _, errs, _ := e.c.SaveNotifyRule(NotifyRule{Name: "x", MinSeverity: "info", MailTo: "kaputt", EscalateMinutes: 5, QuietStart: "25:00"}); errs["channels"] == "" ||
		errs["min_severity"] == "" || errs["mail_to"] == "" || errs["escalate_mail_to"] == "" || errs["quiet_start"] == "" {
		t.Fatalf("validation: %v", errs)
	}

	// warn on the server: only rule a (mail to it@)
	e.report(t, srv, svcState(model.StateWarn))
	e.wait()
	if l := s.take(); len(l) != 1 || l[0].mail != "it@example.org" || l[0].telegram || !strings.HasPrefix(l[0].title, "Warnung: srv") {
		t.Fatalf("server warn: %+v", l)
	}
	// "Wieder OK" of the pc alert (opened before the rules): no rule notified it
	e.report(t, pc, svcState(model.StateOK))
	e.wait()
	if l := s.take(); len(l) != 0 {
		t.Fatalf("pc ok: %+v", l)
	}
	// warn on the pc: no rule for warnings outside the folder
	e.report(t, pc, svcState(model.StateWarn))
	e.wait()
	if l := s.take(); len(l) != 0 {
		t.Fatalf("pc warn: %+v", l)
	}

	// 31 minutes not acknowledged: escalation (once)
	e.at(start.Add(31 * time.Minute))
	e.c.notifyTick()
	e.c.notifyTick()
	e.wait()
	if l := s.take(); len(l) != 1 || l[0].mail != "chef@example.org" || !strings.HasPrefix(l[0].title, "Eskalation: srv – App seit ") {
		t.Fatalf("escalation: %+v", l)
	}
	// 61 minutes: reminder to it@
	e.at(start.Add(61 * time.Minute))
	e.c.notifyTick()
	e.wait()
	if l := s.take(); len(l) != 1 || l[0].mail != "it@example.org" || !strings.HasPrefix(l[0].title, "Weiterhin Warnung: srv") {
		t.Fatalf("reminder: %+v", l)
	}
	// worse: both rules, one message
	e.report(t, srv, svcState(model.StateCrit))
	e.wait()
	if l := s.take(); len(l) != 1 || l[0].mail != "it@example.org" || !l[0].telegram {
		t.Fatalf("crit: %+v", l)
	}
	// acknowledged: no more reminders
	e.c.mu.Lock()
	for _, oa := range e.c.open {
		if oa.a.DeviceID == srv {
			oa.a.AckedBy = "admin"
		}
	}
	e.c.mu.Unlock()
	e.at(start.Add(200 * time.Minute))
	e.c.notifyTick()
	e.wait()
	if l := s.take(); len(l) != 0 {
		t.Fatalf("after ack: %+v", l)
	}
	// ok: to both rules that got it
	e.report(t, srv, svcState(model.StateOK))
	e.wait()
	if l := s.take(); len(l) != 1 || l[0].mail != "it@example.org" || !l[0].telegram || !strings.HasPrefix(l[0].title, "Wieder OK") {
		t.Fatalf("ok: %+v", l)
	}
	// the legacy reminder is off with rules
	e.c.settings.General.RenotifyHours = 1
	e.at(start.Add(400 * time.Minute))
	e.c.renotify()
	e.wait()
	if l := s.take(); len(l) != 0 {
		t.Fatalf("legacy renotify: %+v", l)
	}
	if v := e.c.NotifyRules(); len(v) != 2 || v[0].Name != "Kritisch" || v[0].Matches != 2 {
		t.Fatalf("list: %+v", v)
	}
	if err := e.c.DeleteNotifyRule(a.ID); err != nil {
		t.Fatal(err)
	}
}

func TestNotifyQuietDigest(t *testing.T) {
	e, s := notifyEnv(t)
	pc := e.agentDevice(t, "pc", nil)
	day := time.Date(2026, 11, 11, 0, 0, 0, 0, time.Local) // Wednesday
	night, errs, err := e.c.SaveNotifyRule(NotifyRule{Name: "Nacht", Enabled: true, MinSeverity: model.StateWarn, Mail: true,
		QuietStart: "22:00", QuietEnd: "7:00", QuietDays: []int{1, 2, 3, 4, 5}, QuietCritical: true, Digest: true})
	if err != nil || len(errs) > 0 || night.QuietEnd != "07:00" || night.Quiet != "22:00–07:00 Uhr, beginnt Mo, Di, Mi, Do, Fr" || night.QuietNow {
		t.Fatalf("rule: %+v %v %v", night, errs, err)
	}
	cpu := func(state, msg string) model.Check {
		return model.Check{Key: "cpu", Name: "CPU", Group: "System", State: state, Message: msg}
	}
	// 23:00: the warning is held
	e.at(day.Add(23 * time.Hour))
	e.report(t, pc, svcState(model.StateWarn), cpu(model.StateOK, ""))
	e.wait()
	if l := s.take(); len(l) != 0 {
		t.Fatalf("held: %+v", l)
	}
	// 01:00: fixed – no "Wieder OK" at night; a critical alert goes out at once (QuietCritical)
	e.at(day.Add(25 * time.Hour))
	e.report(t, pc, svcState(model.StateOK), cpu(model.StateCrit, "CPU kaputt"))
	e.wait()
	if l := s.take(); len(l) != 1 || !strings.HasPrefix(l[0].title, "Kritisch: pc – CPU") {
		t.Fatalf("night crit: %+v", l)
	}
	// 02:00: the CPU alert goes back to warn and stays open
	e.at(day.Add(26 * time.Hour))
	e.report(t, pc, svcState(model.StateOK), cpu(model.StateWarn, "CPU hoch"))
	e.c.notifyTick()
	e.wait()
	if l := s.take(); len(l) != 0 {
		t.Fatalf("at night: %+v", l)
	}

	// 07:00: the summary – the app warning (fixed) and the CPU alert (open)
	e.at(day.Add(31*time.Hour + 30*time.Second))
	e.c.notifyTick()
	e.c.notifyTick() // once
	e.wait()
	l := s.take()
	if len(l) != 1 || l[0].mail != "admin@example.org" || l[0].title != "Ruhezeit 22:00–07:00 Uhr: 2 Alarme, 1 noch offen" {
		t.Fatalf("digest: %+v", l)
	}
	rows := l[0].m.Details.Rows
	if len(rows) != 2 || rows[0].Name != "App" || rows[0].Status != "ok" || !strings.HasPrefix(rows[0].Info, "Behoben um 01:00") ||
		rows[1].Name != "CPU" || rows[1].Status != "warning" || !strings.HasPrefix(rows[1].Info, "Noch offen") {
		t.Fatalf("rows: %+v", rows)
	}
	evs, err := e.st.Events(context.Background(), "info", 0, 100)
	if err != nil || !slices.ContainsFunc(evs, func(ev store.Event) bool { return ev.Title == "Zusammenfassung der Ruhezeit gesendet" }) {
		t.Fatalf("no event: %v", err)
	}
	// after the quiet time the open alert counts as notified (summary): no extra message
	e.c.notifyTick()
	e.wait()
	if l := s.take(); len(l) != 0 {
		t.Fatalf("after digest: %+v", l)
	}
	// a quiet time without alerts sends nothing
	e.report(t, pc, svcState(model.StateOK), cpu(model.StateOK, ""))
	e.wait()
	if l := s.take(); len(l) != 1 || !strings.HasPrefix(l[0].title, "Wieder OK: pc – CPU") {
		t.Fatalf("cpu ok in the day: %+v", l)
	}
	e.at(day.Add(55 * time.Hour)) // Friday 07:00
	e.c.notifyTick()
	e.wait()
	if l := s.take(); len(l) != 0 {
		t.Fatalf("empty night: %+v", l)
	}
	sat := NotifyRule{QuietStart: "22:00", QuietEnd: "07:00", QuietDays: []int{1, 2, 3, 4, 5}}
	if sat.holds(day.AddDate(0, 0, 3).Add(23*time.Hour), model.StateWarn) { // Saturday 23:00
		t.Fatal("quiet on Saturday")
	}
	if !sat.holds(day.AddDate(0, 0, 3).Add(3*time.Hour), model.StateWarn) { // Saturday 03:00 (Friday night)
		t.Fatal("not quiet Friday night")
	}
}

func TestAlertLink(t *testing.T) {
	for key, want := range map[string]string{"updates": "/updates", "pve:updates:pve1": "/updates", "ev:oom": "/eventlog", "eventlog": "/eventlog",
		"disk:/": "/devices/d1", "reach": "/devices/d1"} {
		if got := alertLink("d1", key); got != want {
			t.Errorf("%s: %s", key, got)
		}
	}
}
