package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"pk95monitoring/internal/model"
	"pk95monitoring/internal/notify"
)

func TestReportDue(t *testing.T) {
	loc := time.Local
	at := func(s string) time.Time { tm, _ := time.ParseInLocation("2006-01-02 15:04", s, loc); return tm }
	for _, tc := range []struct {
		now, last, want string
	}{
		{"2026-11-01 05:59", "", ""},        // before 06:00
		{"2026-11-01 06:00", "", "2026-10"}, // due
		{"2026-11-01 06:00", "2026-10", ""}, // sent already
		{"2026-11-17 12:00", "2026-09", "2026-10"},
		{"2027-01-01 07:00", "2026-11", "2026-12"}, // over the year
	} {
		m, due := reportDue(at(tc.now), tc.last)
		got := ""
		if due {
			got = m.Format("2006-01")
		}
		if got != tc.want {
			t.Errorf("reportDue(%s, %q) = %q, want %q", tc.now, tc.last, got, tc.want)
		}
	}
}

func TestMonthlyReportMail(t *testing.T) {
	e := newEnv(t, Options{})
	oct := time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local)
	web := e.agentDevice(t, "web01", func(d *model.Device) { d.Created = oct.Unix() })
	e.agentDevice(t, "db01", func(d *model.Device) { d.Created = oct.Unix() })
	id, _ := e.st.OpenAlert(model.Alert{DeviceID: web, DeviceName: "web01", CheckKey: reachKey, CheckName: "Erreichbarkeit", Severity: model.StateCrit, Started: oct.Add(48 * time.Hour).Unix()})
	_ = e.st.CloseAlert(id, oct.Add(50*time.Hour).Unix())
	id, _ = e.st.OpenAlert(model.Alert{DeviceID: web, DeviceName: "web01", CheckKey: "disk:/", CheckName: "Festplatte /", Severity: model.StateWarn, Started: oct.Add(100 * time.Hour).Unix()})
	_ = e.st.CloseAlert(id, oct.Add(103*time.Hour).Unix())

	m, err := e.c.ReportMessage(context.Background(), oct)
	if err != nil {
		t.Fatal(err)
	}
	d := m.Details
	if m.Title != "Verfügbarkeitsbericht Oktober 2026" || d.Status != "warning" || !strings.HasPrefix(m.Body, "1 von 2 Geräten hatten Ausfälle, zusammen 2 Std.") ||
		!strings.Contains(m.Body, "1 weiteres Gerät lief ohne Ausfall und ohne Alarm.") {
		t.Fatalf("message %q %q %s", m.Title, m.Body, d.Status)
	}
	if len(d.Rows) != 1 || d.Rows[0].ID != "web01" || d.Rows[0].Status != "warning" ||
		!strings.Contains(d.Rows[0].Info, "1 Ausfall, zusammen 2 Std., längster 2 Std.") || !strings.Contains(d.Rows[0].Info, "2 Alarme, Problemzeit 3 Std. (Festplatte /)") {
		t.Fatalf("rows %+v", d.Rows)
	}
	if d.Link != "/reports?p=custom&from=2026-10-01&to=2026-10-31" || d.Facts[0].Value != "01.10.2026 – 31.10.2026" {
		t.Fatalf("link %q facts %+v", d.Link, d.Facts)
	}
	csv := string(m.Attachments[0].Data)
	if m.Attachments[0].Name != "verfuegbarkeit-2026-10.csv" || !strings.HasPrefix(csv, "\ufeffGerät;Typ;Gruppe;Verfügbarkeit %") || strings.Count(csv, "\n") != 3 ||
		!strings.Contains(csv, "web01;Agent;;99,73") || !strings.Contains(csv, ";1;120;120;2;180\n") { // 744 or 745 h (summer time ends in October)
		t.Fatalf("csv %q", csv)
	}
	html := notify.HTML(m)
	if !strings.Contains(html, "Geräte mit Ausfällen oder Alarmen") || !strings.Contains(html, "von PK95 Monitoring verschickt") {
		t.Fatalf("html %s", html)
	}

	// Settings: SMTP and recipients are needed; the report has its own recipients.
	s := e.c.Settings()
	s.Report.Enabled = true
	if f, _ := e.c.SaveSettings(s); f["report.enabled"] == "" {
		t.Fatalf("enabled without SMTP: %v", f)
	}
	s.Notify.SMTPHost, s.Notify.MailFrom = "smtp.example.org", "mon@example.org"
	s.Report.MailTo = "chef@example.org, it@example.org"
	if f, err := e.c.SaveSettings(s); err != nil || len(f) > 0 {
		t.Fatal(f, err)
	}
	var sent [][]string
	e.c.Mail = func(_ notify.Settings, m notify.Message, to []string) error { sent = append(sent, to); return nil }

	// Due once from the 1st at 06:00 on.
	e.c.Now = func() time.Time { return time.Date(2026, 11, 1, 7, 0, 0, 0, time.Local) }
	e.c.maybeMonthlyReport()
	e.c.maybeMonthlyReport()
	if len(sent) != 1 || strings.Join(sent[0], ",") != "chef@example.org,it@example.org" {
		t.Fatalf("sent %v", sent)
	}
	var last string
	_ = e.st.GetSetting(keyReportLast, &last)
	if last != "2026-10" {
		t.Fatalf("last %q", last)
	}
	// Off: nothing is sent.
	s.Report.Enabled = false
	_, _ = e.c.SaveSettings(s)
	e.c.Now = func() time.Time { return time.Date(2026, 12, 1, 7, 0, 0, 0, time.Local) }
	e.c.maybeMonthlyReport()
	if len(sent) != 1 {
		t.Fatalf("sent while off %v", sent)
	}
}
