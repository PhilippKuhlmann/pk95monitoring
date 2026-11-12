package notify

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html/template"
	"strings"
	"time"
)

// palette of the three states (PK95 Core: green-600/amber-500/red-600 with their washes).
var tones = map[string]struct{ Accent, Wash, Ink, Word, Mark string }{
	"ok":      {"#16a34a", "#dcfce7", "#14532d", "Erfolgreich", "✓"},
	"warning": {"#d97706", "#fef3c7", "#78350f", "Warnung", "!"},
	"error":   {"#dc2626", "#fee2e2", "#7f1d1d", "Fehler", "✕"},
}

// Subject renders the mail subject with a status mark.
func Subject(m Message) string {
	mark := map[string]string{"ok": "✅", "warning": "⚠️", "error": "❌"}[m.Details.StatusOf(m.Severity)]
	return "[PK95 Monitoring] " + mark + " " + m.Title
}

func rowStatus(s string) string {
	if _, ok := tones[s]; ok {
		return s
	}
	return "ok"
}

func (m Message) counts() (ok, warn, fail int) {
	if m.Details == nil {
		return
	}
	for _, r := range m.Details.Rows {
		switch rowStatus(r.Status) {
		case "ok":
			ok++
		case "warning":
			warn++
		default:
			fail++
		}
	}
	return
}

// Text renders the plain text version (mail fallback, push messages).
func Text(m Message) string {
	var b strings.Builder
	d := m.Details
	b.WriteString(m.Title + "\n")
	if m.Body != "" {
		b.WriteString(m.Body + "\n")
	}
	if d != nil {
		if len(d.Facts) > 0 {
			b.WriteString("\n")
			for _, f := range d.Facts {
				fmt.Fprintf(&b, "%-10s %s\n", f.Label+":", f.Value)
			}
		}
		if len(d.Rows) > 0 {
			title := d.RowsTitle
			if title == "" {
				title = "Einträge"
			}
			b.WriteString("\n" + title + ":\n")
			for _, r := range d.Rows {
				mark := map[string]string{"ok": "OK  ", "warning": "WARN", "error": "FEHL"}[rowStatus(r.Status)]
				line := fmt.Sprintf("  %s %s %s %s", mark, r.Kind, r.ID, r.Name)
				var extra []string
				for _, x := range []string{r.Duration, r.Size, timeShort(r.Time), r.Info} {
					if x != "" {
						extra = append(extra, x)
					}
				}
				if len(extra) > 0 {
					line += " – " + strings.Join(extra, ", ")
				}
				b.WriteString(strings.TrimRight(line, " ") + "\n")
			}
		}
		if len(d.Log) > 0 {
			b.WriteString("\nTechnische Details für Ihren IT-Betreuer:\n")
			for _, l := range d.Log {
				b.WriteString("  " + l + "\n")
			}
		}
	}
	if d != nil && d.Note != "" {
		b.WriteString("\n" + d.Note + "\n")
	}
	if m.URL != "" {
		b.WriteString("\n" + m.URL + linkPath(m) + "\n")
	}
	return b.String()
}

func linkPath(m Message) string {
	if m.Details != nil && m.Details.Link != "" {
		return m.Details.Link
	}
	return "/events"
}

func timeShort(ts int64) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(ts, 0).Format("02.01. 15:04")
}

var mailTmpl = template.Must(template.New("mail").Funcs(template.FuncMap{
	"tone": func(s string) any { return tones[rowStatus(s)] },
	"when": timeShort,
}).Parse(`<!doctype html>
<html lang="de"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="color-scheme" content="light"><title>{{.M.Title}}</title></head>
<body style="margin:0;padding:0;background:#f3f4f6;font-family:'Space Grotesk',-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#111827;">
<div style="display:none;max-height:0;overflow:hidden;">{{.Preheader}}</div>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f3f4f6;"><tr><td align="center" style="padding:24px 12px;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:680px;background:#ffffff;border:1px solid #e5e7eb;border-radius:4px;">
  <tr><td style="padding:18px 24px;border-bottom:1px solid #e5e7eb;">
    <table role="presentation" width="100%" cellpadding="0" cellspacing="0"><tr>
      <td style="vertical-align:middle;">
        <span style="display:inline-block;width:26px;height:26px;background:#0d1e35;border-radius:3px;vertical-align:middle;text-align:center;line-height:26px;color:#bce0ff;font-weight:700;font-size:15px;">↓</span>
        <span style="font-size:19px;font-weight:700;color:#1f3d6e;vertical-align:middle;margin-left:8px;">PK95 Monitoring</span></td>
      <td align="right" style="font-size:12px;color:#6b7280;font-family:Menlo,Consolas,monospace;">{{.M.Host}}</td>
    </tr></table></td></tr>
  <tr><td style="background:{{.T.Wash}};border-left:6px solid {{.T.Accent}};padding:20px 24px;">
    <table role="presentation" cellpadding="0" cellspacing="0"><tr>
      <td style="vertical-align:top;padding-right:14px;"><div style="width:40px;height:40px;border-radius:20px;background:{{.T.Accent}};color:#ffffff;font-size:22px;font-weight:700;line-height:40px;text-align:center;">{{.T.Mark}}</div></td>
      <td style="vertical-align:top;">
        <div style="font-size:11px;font-weight:700;letter-spacing:0.08em;text-transform:uppercase;color:{{.T.Accent}};">{{.T.Word}} · {{.M.Source}}</div>
        <div style="font-size:20px;font-weight:700;color:{{.T.Ink}};margin-top:2px;line-height:1.3;">{{.M.Title}}</div>
        {{if .M.Body}}<div style="font-size:14px;color:{{.T.Ink}};margin-top:6px;line-height:1.5;white-space:pre-line;">{{.M.Body}}</div>{{end}}
      </td></tr></table></td></tr>
  {{if .HasCounts}}<tr><td style="padding:16px 24px 0;">
    {{if .OK}}<span style="display:inline-block;margin:0 6px 6px 0;padding:4px 10px;border-radius:2px;background:#dcfce7;color:#166534;font-size:12px;font-weight:700;">{{.OK}} erfolgreich</span>{{end}}
    {{if .Warn}}<span style="display:inline-block;margin:0 6px 6px 0;padding:4px 10px;border-radius:2px;background:#fef3c7;color:#78350f;font-size:12px;font-weight:700;">{{.Warn}} mit Warnung</span>{{end}}
    {{if .Fail}}<span style="display:inline-block;margin:0 6px 6px 0;padding:4px 10px;border-radius:2px;background:#fee2e2;color:#991b1b;font-size:12px;font-weight:700;">{{.Fail}} fehlgeschlagen</span>{{end}}
  </td></tr>{{end}}
  {{with .D}}{{if .Facts}}<tr><td style="padding:16px 24px 0;">
    <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="font-size:14px;">
    {{range .Facts}}<tr><td style="padding:5px 16px 5px 0;color:#6b7280;white-space:nowrap;vertical-align:top;width:1%;border-bottom:1px solid #f3f4f6;">{{.Label}}</td><td style="padding:5px 0;color:#111827;border-bottom:1px solid #f3f4f6;">{{.Value}}</td></tr>{{end}}
    </table></td></tr>{{end}}
  {{if .Rows}}<tr><td style="padding:20px 24px 0;">
    <div style="font-size:15px;font-weight:700;color:#111827;margin-bottom:8px;">{{if .RowsTitle}}{{.RowsTitle}}{{else}}Einträge{{end}}</div>
    <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="font-size:13px;border:1px solid #e5e7eb;border-radius:3px;border-collapse:separate;">
      <tr style="background:#f9fafb;">
        <td style="padding:8px 10px;font-size:11px;font-weight:600;text-transform:uppercase;letter-spacing:0.04em;color:#6b7280;">Status</td>
        <td style="padding:8px 10px;font-size:11px;font-weight:600;text-transform:uppercase;letter-spacing:0.04em;color:#6b7280;">Name</td>
        <td style="padding:8px 10px;font-size:11px;font-weight:600;text-transform:uppercase;letter-spacing:0.04em;color:#6b7280;">Details</td>
      </tr>
      {{range .Rows}}{{$t := tone .Status}}<tr>
        <td style="padding:9px 10px;border-top:1px solid #f3f4f6;vertical-align:top;white-space:nowrap;"><span style="display:inline-block;padding:2px 8px;border-radius:2px;background:{{$t.Wash}};color:{{$t.Ink}};font-size:11px;font-weight:700;text-transform:uppercase;letter-spacing:0.03em;">{{$t.Mark}} {{$t.Word}}</span></td>
        <td style="padding:9px 10px;border-top:1px solid #f3f4f6;vertical-align:top;"><span style="display:inline-block;padding:0 5px;border-radius:2px;background:#f3f4f6;color:#374151;font-family:Menlo,Consolas,monospace;font-size:11px;">{{.Kind}}</span> <b>{{.ID}}</b> {{.Name}}</td>
        <td style="padding:9px 10px;border-top:1px solid #f3f4f6;vertical-align:top;color:#374151;">{{if .Duration}}{{.Duration}}{{end}}{{if .Size}}{{if .Duration}} · {{end}}{{.Size}}{{end}}{{if .Time}}{{if or .Duration .Size}} · {{end}}{{when .Time}}{{end}}{{if .Info}}<div style="color:{{if eq .Status "ok"}}#6b7280{{else}}{{$t.Accent}}{{end}};font-size:12px;margin-top:2px;">{{.Info}}</div>{{end}}</td>
      </tr>{{end}}
    </table></td></tr>{{end}}
  {{if .Log}}<tr><td style="padding:20px 24px 0;">
    <div style="font-size:13px;font-weight:700;color:#6b7280;margin-bottom:6px;">Technische Details für Ihren IT-Betreuer</div>
    <div style="background:#f9fafb;color:#374151;border:1px solid #e5e7eb;border-radius:3px;padding:10px 12px;font-family:Menlo,Consolas,monospace;font-size:11.5px;line-height:1.55;white-space:pre-wrap;word-break:break-word;">{{range .Log}}{{.}}
{{end}}</div></td></tr>{{end}}{{end}}
  {{with .D}}{{if .Images}}<tr><td style="padding:20px 24px 0;">
    <div style="font-size:15px;font-weight:700;color:#111827;margin-bottom:8px;">Bildschirm nach dem Start</div>
    {{range .Images}}{{$t := tone .Status}}<div style="margin:0 0 14px;border:1px solid #e5e7eb;border-radius:3px;overflow:hidden;">
      <img src="{{index $.Imgs .CID}}" alt="{{.Caption}}" width="630" style="display:block;width:100%;max-width:630px;height:auto;background:#000;">
      <div style="padding:7px 10px;font-size:12px;color:#374151;background:#f9fafb;"><span style="display:inline-block;padding:1px 6px;border-radius:2px;background:{{$t.Wash}};color:{{$t.Ink}};font-weight:700;font-size:10px;text-transform:uppercase;">{{$t.Mark}} {{$t.Word}}</span> {{.Caption}}</div></div>{{end}}
  </td></tr>{{end}}{{if .Note}}<tr><td style="padding:16px 24px 0;font-size:14px;color:#374151;">📎 {{.Note}}</td></tr>{{end}}{{end}}
  {{if .Link}}<tr><td style="padding:24px 24px 0;">
    <a href="{{.Link}}" style="display:inline-block;background:#1f73d6;color:#ffffff;text-decoration:none;font-weight:700;font-size:14px;padding:10px 18px;border-radius:3px;">Details ansehen</a></td></tr>{{end}}
  <tr><td style="padding:24px;"><div style="border-top:1px solid #e5e7eb;padding-top:14px;font-size:12px;color:#6b7280;line-height:1.6;">
    Diese Nachricht wurde automatisch von Ihrer Datensicherung verschickt ({{.When}}).<br>
    Bei Fragen wenden Sie sich bitte an Ihren IT-Betreuer.<br>
    <span style="color:#9ca3af;">{{.M.Host}}{{if .M.Version}} · PK95 Monitoring {{.M.Version}}{{end}}</span></div></td></tr>
</table></td></tr></table></body></html>`))

// HTML renders the mail body (images as cid: references to the inline attachments).
func HTML(m Message) string { return renderHTML(m, false) }

// HTMLPreview renders the mail for a browser: inline images become data: URLs.
func HTMLPreview(m Message) string { return renderHTML(m, true) }

func renderHTML(m Message, preview bool) string {
	ok, warn, fail := m.counts()
	t := m.Time
	if t.IsZero() {
		t = time.Now()
	}
	link := ""
	if m.URL != "" {
		link = m.URL + linkPath(m)
	}
	pre := m.Body
	if pre == "" {
		pre = m.Title
	}
	data := map[string]any{
		"M": m, "D": m.Details, "T": tones[m.Details.StatusOf(m.Severity)],
		"OK": ok, "Warn": warn, "Fail": fail, "HasCounts": ok+warn+fail > 1,
		"Link": template.URL(link), "When": t.Format("02.01.2006 um 15:04 Uhr"), "Preheader": pre,
	}
	// image sources: cid: references to the inline attachments, data: URLs for a browser preview
	imgs := map[string]template.URL{}
	for _, a := range m.Attachments {
		if a.ContentID == "" {
			continue
		}
		if preview {
			imgs[a.ContentID] = template.URL("data:" + a.ContentType + ";base64," + base64.StdEncoding.EncodeToString(a.Data))
		} else {
			imgs[a.ContentID] = template.URL("cid:" + a.ContentID)
		}
	}
	data["Imgs"] = imgs
	var b bytes.Buffer
	if err := mailTmpl.Execute(&b, data); err != nil {
		return "<pre>" + template.HTMLEscapeString(Text(m)) + "</pre>"
	}
	return b.String()
}
