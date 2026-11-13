package core

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"pk95monitoring/internal/notify"
)

// The monthly report: on the first of a month (from reportHour on) the availability report of
// the previous month is mailed with the SMTP settings of the notifications – whether or not
// mail notifications are on – to its own recipients (default: the notification recipients).

const (
	keyReportLast = "report.last" // month of the last report sent ("2026-10")
	reportHour    = 6             // local hour from which the report of the 1st goes out
)

// ReportSettings configure the monthly report.
type ReportSettings struct {
	Enabled bool   `json:"enabled"`
	MailTo  string `json:"mail_to"` // comma separated; empty = the notification recipients
}

var monthNames = [...]string{"Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"}

// MonthLabel is "Oktober 2026".
func MonthLabel(t time.Time) string { return monthNames[t.Month()-1] + " " + strconv.Itoa(t.Year()) }

// monthBounds returns the start of the month of t and of the next month (in t's location).
func monthBounds(t time.Time) (time.Time, time.Time) {
	a := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	return a, a.AddDate(0, 1, 0)
}

// ParseMonth reads "2026-10" in the location of now; empty = the month before now.
func ParseMonth(s string, now time.Time) (time.Time, error) {
	if s == "" {
		a, _ := monthBounds(now)
		return a.AddDate(0, -1, 0), nil
	}
	t, err := time.ParseInLocation("2006-01", s, now.Location())
	if err != nil {
		return time.Time{}, errors.New("Monat im Format JJJJ-MM angeben")
	}
	return t, nil
}

// reportRecipients are the addresses of the monthly report.
func (s Settings) reportRecipients() []string {
	if to := notify.Recipients(s.Report.MailTo); len(to) > 0 {
		return to
	}
	return notify.Recipients(s.Notify.MailTo)
}

// validateReport checks the monthly report settings (keys "report.*").
func validateReport(s *Settings, errs map[string]string) {
	r := &s.Report
	r.MailTo = strings.TrimSpace(r.MailTo)
	for _, a := range notify.Recipients(r.MailTo) {
		if !strings.Contains(a, "@") || strings.ContainsAny(a, " <>\r\n") {
			errs["report.mail_to"] = "Ungültige Adresse: " + a
			break
		}
	}
	if r.Enabled {
		switch {
		case s.Notify.SMTPHost == "" || s.Notify.MailFrom == "":
			errs["report.enabled"] = "Zuerst unter „E-Mail“ den SMTP-Server und den Absender eintragen."
		case len(s.reportRecipients()) == 0:
			errs["report.mail_to"] = "Mindestens einen Empfänger angeben."
		}
	}
}

// ReportMessage builds the mail of the availability report of the month starting at month.
func (c *Core) ReportMessage(ctx context.Context, month time.Time) (notify.Message, error) {
	from, to := monthBounds(month)
	rep, err := c.Report(ctx, from.Unix(), to.Unix())
	if err != nil {
		return notify.Message{}, err
	}
	devs := rep.Devices
	var withOutages, belowCrit int
	for _, d := range devs {
		if d.Outages > 0 {
			withOutages++
		}
		if d.Availability < 99 {
			belowCrit++
		}
	}
	last := to.AddDate(0, 0, -1)
	d := &notify.Details{Status: "ok", Link: fmt.Sprintf("/reports?p=custom&from=%s&to=%s", from.Format("2006-01-02"), last.Format("2006-01-02")),
		RowsTitle: "Geräte mit Ausfällen oder Alarmen", CountWords: [3]string{"ohne Ausfall", "mit Ausfällen", "unter 99 %"}}
	switch {
	case belowCrit > 0:
		d.Status = "error"
	case withOutages > 0:
		d.Status = "warning"
	}
	avail := "—"
	if rep.Availability != nil {
		avail = pctText(*rep.Availability)
	}
	d.Facts = []notify.Fact{
		{Label: "Zeitraum", Value: from.Format("02.01.2006") + " – " + last.Format("02.01.2006")},
		{Label: "Verfügbarkeit", Value: avail + " über alle Geräte"},
		{Label: "Geräte", Value: strconv.Itoa(len(devs))},
		{Label: "Ausfälle", Value: fmt.Sprintf("%d (zusammen %s)", rep.Outages, durText(rep.Downtime))},
		{Label: "Alarme", Value: strconv.Itoa(rep.Alerts)},
	}
	quiet := 0
	for _, dv := range devs {
		if dv.Outages == 0 && dv.Alerts == 0 {
			quiet++
			continue
		}
		st := "ok"
		switch {
		case dv.Availability < 99:
			st = "error"
		case dv.Outages > 0:
			st = "warning"
		}
		info := []string{"Verfügbarkeit " + pctText(dv.Availability)}
		if dv.Outages > 0 {
			info = append(info, fmt.Sprintf("%d %s, zusammen %s, längster %s", dv.Outages, plural1(dv.Outages, "Ausfall", "Ausfälle"), durText(dv.Downtime), durText(dv.Longest)))
		}
		if dv.Alerts > 0 {
			line := fmt.Sprintf("%d %s", dv.Alerts, plural1(dv.Alerts, "Alarm", "Alarme"))
			if dv.ProblemTime > 0 {
				line += ", Problemzeit " + durText(dv.ProblemTime)
			}
			if len(dv.Checks) > 0 {
				names := make([]string, 0, 4)
				for i, ck := range dv.Checks {
					if i == 3 {
						names = append(names, "…")
						break
					}
					names = append(names, ck.Name)
				}
				line += " (" + strings.Join(names, ", ") + ")"
			}
			info = append(info, line)
		}
		d.Rows = append(d.Rows, notify.Row{Kind: typeLabel(dv.Type), ID: dv.Name, Status: st, Info: strings.Join(info, " · ")})
	}
	var body string
	switch {
	case len(devs) == 0:
		body = "Im Zeitraum wurden keine Geräte überwacht."
	case withOutages == 0:
		body = fmt.Sprintf("Alle %d %s den ganzen Monat erreichbar.", len(devs), plural1(len(devs), "Gerät war", "Geräte waren"))
	default:
		body = fmt.Sprintf("%d von %d Geräten hatten Ausfälle, zusammen %s.", withOutages, len(devs), durText(rep.Downtime))
	}
	if quiet > 0 && len(d.Rows) > 0 {
		body += fmt.Sprintf(" %d %s ohne Ausfall und ohne Alarm.", quiet, plural1(quiet, "weiteres Gerät lief", "weitere Geräte liefen"))
	}
	d.Note = "Die vollständige Liste aller Geräte hängt als CSV-Datei an."
	m := c.message("info", "Monatsbericht", "Verfügbarkeitsbericht "+MonthLabel(month), body, d)
	m.Attachments = []notify.Attachment{{Name: "verfuegbarkeit-" + month.Format("2006-01") + ".csv", ContentType: "text/csv; charset=utf-8", Data: reportCSV(rep)}}
	return m, nil
}

// reportCSV is the full report as CSV (semicolons and decimal commas for German spreadsheets).
func reportCSV(rep Report) []byte {
	var b bytes.Buffer
	b.WriteString("\ufeff") // BOM: spreadsheets open UTF-8 correctly
	w := csv.NewWriter(&b)
	w.Comma = ';'
	_ = w.Write([]string{"Gerät", "Typ", "Gruppe", "Verfügbarkeit %", "Ausfälle", "Ausfallzeit (Min.)", "Längster Ausfall (Min.)", "Alarme", "Problemzeit (Min.)"})
	minutes := func(s int64) string { return strconv.FormatInt((s+30)/60, 10) }
	for _, d := range rep.Devices {
		_ = w.Write([]string{d.Name, typeLabel(d.Type), d.Group, strings.Replace(strconv.FormatFloat(d.Availability, 'f', 3, 64), ".", ",", 1),
			strconv.Itoa(d.Outages), minutes(d.Downtime), minutes(d.Longest), strconv.Itoa(d.Alerts), minutes(d.ProblemTime)})
	}
	w.Flush()
	return b.Bytes()
}

// SendReport mails the report of the month starting at month now (also the "Jetzt senden"
// button) and remembers the month as sent.
func (c *Core) SendReport(ctx context.Context, month time.Time) error {
	s := c.Settings()
	to := s.reportRecipients()
	if s.Notify.SMTPHost == "" || s.Notify.MailFrom == "" || len(to) == 0 {
		return errors.New("Für den Monatsbericht SMTP-Server, Absender und Empfänger eintragen")
	}
	m, err := c.ReportMessage(ctx, month)
	if err != nil {
		return err
	}
	if err := c.Mail(s.Notify, m, to); err != nil {
		return err
	}
	if err := c.St.SetSetting(keyReportLast, month.Format("2006-01")); err != nil {
		log.Printf("report: %v", err)
	}
	c.Event("info", "Monatsbericht", "Monatsbericht verschickt", fmt.Sprintf("Verfügbarkeitsbericht %s an %s.", MonthLabel(month), strings.Join(to, ", ")), nil, false)
	return nil
}

// reportDue returns the month whose report is due at now: from the 1st at reportHour on the
// previous month, unless it was sent already.
func reportDue(now time.Time, last string) (time.Time, bool) {
	start, _ := monthBounds(now)
	if now.Before(start.Add(reportHour * time.Hour)) {
		return time.Time{}, false
	}
	prev := start.AddDate(0, -1, 0)
	if last == prev.Format("2006-01") {
		return time.Time{}, false
	}
	return prev, true
}

// maybeMonthlyReport sends the report of the previous month once (called hourly).
func (c *Core) maybeMonthlyReport() {
	if !c.Settings().Report.Enabled {
		return
	}
	var last string
	if err := c.St.GetSetting(keyReportLast, &last); err != nil {
		log.Printf("report: %v", err)
		return
	}
	month, due := reportDue(c.Now().Local(), last)
	if !due {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
	defer cancel()
	if err := c.SendReport(ctx, month); err != nil {
		log.Printf("report: %v", err)
		c.Event("warning", "Monatsbericht", "Monatsbericht nicht verschickt", err.Error(), nil, false)
	}
}

// pctText is "99,95 %" (never rounded up to 100 %).
func pctText(v float64) string {
	if v >= 100 {
		return "100 %"
	}
	return strings.Replace(strconv.FormatFloat(float64(int(v*100))/100, 'f', 2, 64), ".", ",", 1) + " %"
}

// durText is "2 Std. 10 Min." (as in the web UI).
func durText(s int64) string {
	if s <= 0 {
		return "0 Min."
	}
	d, h, m := s/86400, s%86400/3600, s%3600/60
	switch {
	case d > 0 && h > 0:
		return fmt.Sprintf("%d %s %d Std.", d, plural1(int(d), "Tag", "Tage"), h)
	case d > 0:
		return fmt.Sprintf("%d %s", d, plural1(int(d), "Tag", "Tage"))
	case h > 0 && m > 0:
		return fmt.Sprintf("%d Std. %d Min.", h, m)
	case h > 0:
		return fmt.Sprintf("%d Std.", h)
	case m > 0:
		return fmt.Sprintf("%d Min.", m)
	}
	return fmt.Sprintf("%d s", s)
}

func plural1(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
