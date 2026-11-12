package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// TelegramAPI is the Bot API base URL (tests point it to a local server).
var TelegramAPI = "https://api.telegram.org"

var (
	telegramTokenRe = regexp.MustCompile(`^\d{5,}:[A-Za-z0-9_-]{30,}$`)
	telegramChatRe  = regexp.MustCompile(`^(-?\d{1,20}|@[A-Za-z][A-Za-z0-9_]{4,31})$`)
)

// ValidTelegramToken checks the form of a bot token ("123456:ABC…").
func ValidTelegramToken(s string) bool { return telegramTokenRe.MatchString(s) }

// ValidTelegramChat checks a chat id (number, groups negative) or a @channel name.
func ValidTelegramChat(s string) bool { return telegramChatRe.MatchString(s) }

// TelegramChat is a chat that wrote to the bot (to pick the chat id).
type TelegramChat struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Type  string `json:"type"` // private, group, supergroup, channel
}

type tgResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

func tgDo(ctx context.Context, token, method string, body io.Reader, contentType string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TelegramAPI+"/bot"+token+"/"+method, body)
	if err != nil {
		return errors.New("Telegram: ungültige Anfrage")
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		// The URL contains the token: never pass it on in the error.
		return fmt.Errorf("Telegram nicht erreichbar: %s", strings.ReplaceAll(err.Error(), token, "…"))
	}
	defer resp.Body.Close()
	var r tgResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&r); err != nil {
		return fmt.Errorf("Telegram: HTTP %d", resp.StatusCode)
	}
	if !r.OK {
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusNotFound:
			return errors.New("Telegram: Bot-Token ungültig")
		}
		return errors.New("Telegram: " + r.Description)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

func tgJSON(ctx context.Context, token, method string, payload any, out any) error {
	b, _ := json.Marshal(payload)
	return tgDo(ctx, token, method, bytes.NewReader(b), "application/json", out)
}

// TelegramChats returns the chats that recently wrote to the bot or added it, so the chat id
// does not have to be looked up by hand.
func TelegramChats(ctx context.Context, token string) ([]TelegramChat, error) {
	type chat struct {
		ID        int64  `json:"id"`
		Type      string `json:"type"`
		Title     string `json:"title"`
		Username  string `json:"username"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
	}
	type withChat struct {
		Chat chat `json:"chat"`
	}
	var updates []struct {
		Message           *withChat `json:"message"`
		EditedMessage     *withChat `json:"edited_message"`
		ChannelPost       *withChat `json:"channel_post"`
		EditedChannelPost *withChat `json:"edited_channel_post"`
		MyChatMember      *withChat `json:"my_chat_member"`
	}
	if err := tgJSON(ctx, token, "getUpdates", map[string]any{"limit": 100, "timeout": 0}, &updates); err != nil {
		if strings.Contains(err.Error(), "webhook") {
			return nil, errors.New("Für diesen Bot ist ein Webhook eingerichtet, dann gibt Telegram keine Nachrichten zum Auslesen heraus. Chat-ID bitte von Hand eintragen.")
		}
		return nil, err
	}
	seen := map[int64]bool{}
	out := []TelegramChat{}
	for i := len(updates) - 1; i >= 0; i-- { // newest first
		u := updates[i]
		for _, w := range []*withChat{u.Message, u.EditedMessage, u.ChannelPost, u.EditedChannelPost, u.MyChatMember} {
			if w == nil || w.Chat.ID == 0 || seen[w.Chat.ID] {
				continue
			}
			seen[w.Chat.ID] = true
			c := w.Chat
			title := c.Title
			if title == "" {
				title = strings.TrimSpace(c.FirstName + " " + c.LastName)
			}
			if c.Username != "" {
				if title == "" {
					title = "@" + c.Username
				} else {
					title += " (@" + c.Username + ")"
				}
			}
			out = append(out, TelegramChat{ID: c.ID, Title: title, Type: c.Type})
		}
	}
	return out, nil
}

var statusEmoji = map[string]string{"ok": "✅", "warning": "⚠️", "error": "❌"}

// telegramText renders the message for Telegram (HTML parse mode, at most 4096 characters).
func telegramText(m Message) string {
	e := html.EscapeString
	head := statusEmoji[m.Details.StatusOf(m.Severity)] + " <b>" + e(m.Title) + "</b>"
	var parts []string
	if body := strings.TrimSpace(m.Body); body != "" {
		parts = append(parts, e(body))
	}
	d := m.Details
	if d != nil && len(d.Facts) > 0 {
		var b strings.Builder
		for _, f := range d.Facts {
			b.WriteString("<b>" + e(f.Label) + ":</b> " + e(f.Value) + "\n")
		}
		parts = append(parts, strings.TrimRight(b.String(), "\n"))
	}
	if d != nil && len(d.Rows) > 0 {
		var b strings.Builder
		if d.RowsTitle != "" {
			b.WriteString("<b>" + e(d.RowsTitle) + "</b>\n")
		}
		for i, r := range d.Rows {
			if i == 40 {
				fmt.Fprintf(&b, "… und %d weitere\n", len(d.Rows)-40)
				break
			}
			line := statusEmoji[r.Status] + " " + e(strings.TrimSpace(r.Kind+" "+r.ID))
			if r.Name != "" {
				line += " <b>" + e(r.Name) + "</b>"
			}
			var extra []string
			for _, x := range []string{r.Duration, r.Size, r.Info} {
				if x != "" {
					extra = append(extra, x)
				}
			}
			if len(extra) > 0 {
				line += " – " + e(strings.Join(extra, ", "))
			}
			b.WriteString(line + "\n")
		}
		parts = append(parts, strings.TrimRight(b.String(), "\n"))
	}
	if d != nil && d.Note != "" {
		parts = append(parts, "<i>"+e(d.Note)+"</i>")
	}
	t := m.Time
	if t.IsZero() {
		t = time.Now()
	}
	foot := "<i>PK95 Monitoring"
	if m.Host != "" {
		foot += " · " + e(m.Host)
	}
	foot += " · " + t.Format("02.01.2006 15:04") + "</i>"
	text := head
	if len(parts) > 0 {
		text += "\n\n" + strings.Join(parts, "\n\n")
	}
	// The log goes last and is shortened to what still fits.
	if d != nil && len(d.Log) > 0 {
		room := 4000 - utf8.RuneCountInString(text) - utf8.RuneCountInString(foot) - 80
		log := strings.Join(d.Log, "\n")
		if room > 200 {
			if utf8.RuneCountInString(log) > room {
				log = string([]rune(log)[:room]) + "\n…"
			}
			text += "\n\n<b>Technische Details für Ihren IT-Betreuer</b>\n<pre>" + e(log) + "</pre>"
		}
	}
	text += "\n\n" + foot
	if utf8.RuneCountInString(text) > 4096 {
		// Very many long rows: title, text and footer only.
		short := head + "\n\n" + e(truncateRunes(strings.TrimSpace(m.Body), 3500))
		text = short + "\n\n" + foot
	}
	return text
}

func sendTelegram(ctx context.Context, s Settings, m Message) error {
	if !ValidTelegramToken(s.TelegramToken) {
		return errors.New("Bot-Token fehlt oder hat die falsche Form")
	}
	if !ValidTelegramChat(s.TelegramChat) {
		return errors.New("Chat-ID fehlt")
	}
	payload := map[string]any{"chat_id": s.TelegramChat, "text": telegramText(m), "parse_mode": "HTML",
		"link_preview_options": map[string]any{"is_disabled": true}}
	if s.MailLinks && m.URL != "" {
		payload["reply_markup"] = map[string]any{"inline_keyboard": [][]map[string]string{{{"text": "Details ansehen", "url": m.URL + linkPath(m)}}}}
	}
	if err := tgJSON(ctx, s.TelegramToken, "sendMessage", payload, nil); err != nil {
		return err
	}
	if !s.TelegramFiles {
		return nil
	}
	// Screenshots as photos, reports as files.
	captions := map[string]string{}
	if m.Details != nil {
		for _, im := range m.Details.Images {
			captions[im.CID] = strings.TrimSpace(statusEmoji[im.Status] + " " + im.Caption)
		}
	}
	photos := 0
	for _, a := range m.Attachments {
		method, field, caption := "sendDocument", "document", a.Name
		if a.ContentID != "" {
			if !strings.HasPrefix(a.ContentType, "image/") || photos >= 10 {
				continue
			}
			photos++
			method, field, caption = "sendPhoto", "photo", captions[a.ContentID]
		}
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		_ = w.WriteField("chat_id", s.TelegramChat)
		if caption != "" {
			_ = w.WriteField("caption", truncateRunes(caption, 1000))
		}
		fw, err := w.CreateFormFile(field, a.Name)
		if err != nil {
			return err
		}
		_, _ = fw.Write(a.Data)
		_ = w.Close()
		if err := tgDo(ctx, s.TelegramToken, method, &b, w.FormDataContentType(), nil); err != nil {
			return fmt.Errorf("Anhang %s: %w", a.Name, err)
		}
	}
	return nil
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
