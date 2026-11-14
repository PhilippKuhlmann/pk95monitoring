package core

import (
	"testing"
	"time"

	"pk95monitoring/internal/model"
)

func TestWindowSchedule(t *testing.T) {
	at := func(m, d, h, min int) time.Time { return time.Date(2026, time.Month(m), d, h, min, 0, 0, time.Local) }
	// November 2026 starts on a Sunday: Tuesdays are the 3rd, 10th, 17th, 24th.
	patch := MaintWindow{Repeat: RepeatMonthly, Week: 2, Weekdays: []int{2}, Offset: 2, Start: "22:00", Minutes: 240}
	if s, ok := patch.next(at(11, 1, 0, 0)); !ok || !s.Equal(at(11, 12, 22, 0)) {
		t.Fatalf("patch day + 2: %v", s)
	}
	if s, _ := patch.next(at(11, 13, 0, 0)); !s.Equal(at(12, 10, 22, 0)) { // Dec: 2nd Tuesday is the 8th
		t.Fatalf("patch day December: %v", s)
	}
	if got := patch.Describe(); got != "2 Tage nach dem 2. Dienstag im Monat, 22:00 Uhr, 4 Std." {
		t.Fatalf("describe %q", got)
	}
	// over midnight: running at 01:00 of the next day
	if s, e, ok := patch.occurrence(at(11, 13, 1, 0)); !ok || !s.Equal(at(11, 12, 22, 0)) || !e.Equal(at(11, 13, 2, 0)) {
		t.Fatalf("occurrence over midnight: %v %v %v", s, e, ok)
	}
	if _, _, ok := patch.occurrence(at(11, 13, 2, 0)); ok {
		t.Fatal("occurrence after its end")
	}

	last := MaintWindow{Repeat: RepeatMonthly, Week: weekLast, Weekdays: []int{0}, Start: "03:00", Minutes: 60}
	if s, _ := last.next(at(11, 1, 0, 0)); !s.Equal(at(11, 29, 3, 0)) {
		t.Fatalf("last Sunday: %v", s)
	}
	if got := last.Describe(); got != "Am letzten Sonntag im Monat, 03:00 Uhr, 1 Std." {
		t.Fatalf("describe %q", got)
	}
	day31 := MaintWindow{Repeat: RepeatMonthly, Day: 31, Start: "01:00", Minutes: 30}
	if s, _ := day31.next(at(11, 1, 0, 0)); !s.Equal(at(11, 30, 1, 0)) {
		t.Fatalf("day 31 in November: %v", s)
	}
	weekly := MaintWindow{Repeat: RepeatWeekly, Weekdays: []int{5, 1}, Start: "12:00", Minutes: 90}
	if s, _ := weekly.next(at(11, 10, 13, 0)); !s.Equal(at(11, 13, 12, 0)) { // Tue → Fri
		t.Fatalf("weekly: %v", s)
	}
	if got := weekly.Describe(); got != "Jeden Mo, Fr, 12:00 Uhr, 1 Std. 30 Min." {
		t.Fatalf("describe %q", got)
	}
	once := MaintWindow{Repeat: RepeatOnce, Date: "2026-11-20", Start: "18:00", Minutes: 120}
	if _, ok := once.next(at(11, 21, 0, 0)); ok {
		t.Fatal("one-time window repeats")
	}
	if got := once.Describe(); got != "Einmalig am 20.11.2026, 18:00 Uhr, 2 Std." {
		t.Fatalf("describe %q", got)
	}
}

func TestWindowValidate(t *testing.T) {
	e := newEnv(t, Options{})
	_, errs, err := e.c.SaveWindow(MaintWindow{Repeat: RepeatMonthly, Week: 2, Start: "25:00", Minutes: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"name", "start", "minutes", "weekdays", "targets"} {
		if errs[k] == "" {
			t.Errorf("no error for %s: %v", k, errs)
		}
	}
	if _, _, err := e.c.SaveWindow(MaintWindow{ID: "nope", Name: "x"}); err == nil {
		t.Fatal("update of an unknown window")
	}
}

func TestWindowApply(t *testing.T) {
	e := newEnv(t, Options{}) // the clock is 2026-11-11 10:00
	a := e.agentDevice(t, "a1", func(d *model.Device) { d.Group = "Server/Nord" })
	b := e.agentDevice(t, "b1", nil)
	crit := model.Check{Key: "svc", Name: "Dienst", Group: "Dienste", State: model.StateCrit, Message: "gestoppt"}

	v, errs, err := e.c.SaveWindow(MaintWindow{Name: "Updates", Enabled: true, Repeat: RepeatDaily, Start: "9:30", Minutes: 60,
		Folders: []string{" Server "}, Note: "Patchen"})
	if err != nil || len(errs) > 0 {
		t.Fatalf("save: %v %v", errs, err)
	}
	if v.Start != "09:30" || v.Matches != 1 || v.ActiveUntil != time.Date(2026, 11, 11, 10, 30, 0, 0, time.Local).Unix() {
		t.Fatalf("view %+v", v)
	}
	da, _ := e.c.Device(a)
	if da.Maintenance == nil || da.Maintenance.Window != v.ID || da.Maintenance.Note != "Wartungsfenster „Updates“ – Patchen" {
		t.Fatalf("maintenance of a: %+v", da.Maintenance)
	}
	if db, _ := e.c.Device(b); db.Maintenance != nil {
		t.Fatalf("b in maintenance: %+v", db.Maintenance)
	}
	if body := lastEventBody(t, e, "Wartungsfenster begonnen"); body == "" {
		t.Fatal("no begin event")
	}
	e.report(t, a, crit)
	if l, _ := e.st.ActiveAlerts(); len(l) != 0 {
		t.Fatalf("alert during the window: %+v", l)
	}
	if ws := e.c.DeviceWindows(a); len(ws) != 1 || ws[0].ID != v.ID {
		t.Fatalf("device windows %+v", ws)
	}

	// ended by hand: the window does not start it again in this occurrence
	if _, err := e.c.SetMaintenance(a, 0, ""); err != nil {
		t.Fatal(err)
	}
	e.c.applyWindows()
	if da, _ := e.c.Device(a); da.InMaintenance(e.clk.Now().Unix()) {
		t.Fatal("maintenance started again after it was ended by hand")
	}
	e.report(t, a, crit)
	if l, _ := e.st.ActiveAlerts(); len(l) != 1 {
		t.Fatalf("alerts after ending by hand: %d", len(l))
	}

	// the next day it applies again; a longer maintenance by hand is kept
	e.clk.Add(24 * time.Hour)
	until := e.clk.Now().Add(5 * time.Hour).Unix()
	if _, err := e.c.SetMaintenance(b, until, "lang"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.c.SaveWindow(MaintWindow{ID: v.ID, Name: "Updates", Enabled: true, Repeat: RepeatDaily, Start: "09:30",
		Minutes: 60, Folders: []string{"Server"}, Devices: []string{b, "gone"}}); err != nil {
		t.Fatal(err)
	}
	if da, _ := e.c.Device(a); !da.InMaintenance(e.clk.Now().Unix()) {
		t.Fatal("not in maintenance the next day")
	}
	if db, _ := e.c.Device(b); db.Maintenance.Until != until || db.Maintenance.Window != "" {
		t.Fatalf("longer maintenance replaced: %+v", db.Maintenance)
	}
	if l, _ := e.st.ActiveAlerts(); len(l) != 0 {
		t.Fatalf("alert not closed by the window: %d", len(l))
	}

	// after the end: alerts again and an end event
	e.clk.Add(31 * time.Minute)
	e.c.applyWindows()
	if da, _ := e.c.Device(a); da.InMaintenance(e.clk.Now().Unix()) {
		t.Fatal("still in maintenance after the window")
	}
	if body := lastEventBody(t, e, "Wartungsfenster beendet"); body != "Updates: Alarme sind wieder aktiv." {
		t.Fatalf("end event %q", body)
	}

	// deleting a running window ends its maintenance
	e.clk.Add(23*time.Hour + 29*time.Minute) // 10:00 the next day
	e.c.applyWindows()
	if err := e.c.DeleteWindow(v.ID); err != nil {
		t.Fatal(err)
	}
	if da, _ := e.c.Device(a); da.Maintenance != nil {
		t.Fatalf("maintenance left after delete: %+v", da.Maintenance)
	}
	if len(e.c.Windows()) != 0 {
		t.Fatal("window not deleted")
	}
}
