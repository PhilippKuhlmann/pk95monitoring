package notify

import (
	"strings"
	"testing"
)

func sample(status string) Message {
	return Message{Severity: "warning", Source: "Cluster", Title: "Sicherung mit Fehlern: 1 von 2 gesichert", Body: "1 von 2", Host: "pk95", URL: "https://10.0.0.5",
		Details: &Details{Status: status, RowsTitle: "Gesicherte VMs und Container", Link: "/devices/pve",
			Facts: []Fact{{Label: "Ziel", Value: "pbs-backup"}},
			Rows:  []Row{{Kind: "VM", ID: "100", Name: "dc<01>", Status: "ok", Size: "32 GiB"}, {Kind: "CT", ID: "110", Name: "pihole", Status: "error", Info: "kaputt"}},
			Log:   []string{"ERROR: Backup of VM 110 failed - kaputt"}}}
}

func TestHTMLColorsAndContent(t *testing.T) {
	for status, color := range map[string]string{"ok": "#16a34a", "warning": "#d97706", "error": "#dc2626"} {
		h := HTML(sample(status))
		if !strings.Contains(h, "border-left:6px solid "+color) {
			t.Errorf("%s: band color %s missing", status, color)
		}
	}
	h := HTML(sample("warning"))
	for _, want := range []string{"dc&lt;01&gt;", "pihole", "kaputt", "pbs-backup", "https://10.0.0.5/devices/pve", "1 erfolgreich", "1 fehlgeschlagen", "Technische Details für Ihren IT-Betreuer"} {
		if !strings.Contains(h, want) {
			t.Errorf("html misses %q", want)
		}
	}
	if strings.Contains(h, "dc<01>") {
		t.Error("guest name not escaped")
	}
	if s := Subject(sample("error")); !strings.Contains(s, "❌") {
		t.Errorf("subject %q", s)
	}
	txt := Text(sample("warning"))
	if !strings.Contains(txt, "FEHL CT 110 pihole – kaputt") || !strings.Contains(txt, "OK   VM 100 dc<01> – 32 GiB") {
		t.Errorf("text:\n%s", txt)
	}
}

func TestBuildMailWithImagesAndAttachment(t *testing.T) {
	m := sample("ok")
	m.Details.Images = []Image{{CID: "shot-100@pk95", Caption: "100 dc01", Status: "ok"}}
	m.Details.Note = "Der vollständige Bericht hängt an."
	m.Attachments = []Attachment{
		{Name: "vm-100.jpg", ContentType: "image/jpeg", Data: []byte("JPEGDATA"), ContentID: "shot-100@pk95"},
		{Name: "Restore-Test.html", ContentType: "text/html; charset=utf-8", Data: []byte("<html>Bericht</html>")},
	}
	raw := string(BuildMail(Settings{MailFrom: "a@x.de"}, m, []string{"kunde@x.de"}))
	for _, want := range []string{"multipart/mixed", "multipart/related", "multipart/alternative", "Content-ID: <shot-100@pk95>",
		"Content-Disposition: inline; filename=\"vm-100.jpg\"", "Content-Disposition: attachment; filename=\"Restore-Test.html\""} {
		if !strings.Contains(raw, want) {
			t.Errorf("mail misses %q", want)
		}
	}
	html := HTML(m)
	if !strings.Contains(html, `src="cid:shot-100@pk95"`) || !strings.Contains(html, "Der vollständige Bericht hängt an.") {
		t.Error("html must reference the inline image and mention the attachment")
	}
	// Without MailLinks the mail has no button and no link to the appliance.
	if strings.Contains(raw, base64Of("Details ansehen")) || strings.Contains(raw, "10.0.0.5") {
		t.Error("link must not be in the mail when MailLinks is off")
	}
	withLinks := BuildMail(Settings{MailFrom: "a@x.de", MailLinks: true}, m, []string{"x@x.de"})
	if len(withLinks) == 0 {
		t.Fatal("empty")
	}
	if p := HTMLPreview(m); !strings.Contains(p, "data:image/jpeg;base64,") {
		t.Error("preview must inline the image")
	}
}

func base64Of(s string) string { return b64(s)[:12] }

func TestLinkPathFallsBackToEvents(t *testing.T) {
	if got := linkPath(Message{}); got != "/events" {
		t.Errorf("linkPath without details = %q, want /events", got)
	}
	if got := linkPath(Message{Details: &Details{Link: "/devices/x"}}); got != "/devices/x" {
		t.Errorf("linkPath with details = %q", got)
	}
}

func TestCountWords(t *testing.T) {
	m := Message{Severity: "info", Title: "Bericht", Details: &Details{CountWords: [3]string{"ohne Ausfall", "mit Ausfällen", "unter 99 %"},
		Rows: []Row{{ID: "a", Status: "ok"}, {ID: "b", Status: "warning"}}}}
	h := HTML(m)
	if !strings.Contains(h, "1 ohne Ausfall") || !strings.Contains(h, "1 mit Ausfällen") || strings.Contains(h, "erfolgreich") {
		t.Fatalf("count words: %s", h)
	}
}
