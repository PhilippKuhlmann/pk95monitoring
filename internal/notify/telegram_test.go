package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

const testToken = "123456789:AAH-abcdefghijklmnopqrstuvwxyz012345"

func fakeTelegram(t *testing.T, handle func(method string, r *http.Request) string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/bot"+testToken+"/") {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"ok":false,"description":"Unauthorized"}`)
			return
		}
		_, _ = io.WriteString(w, handle(strings.TrimPrefix(r.URL.Path, "/bot"+testToken+"/"), r))
	}))
	old := TelegramAPI
	TelegramAPI = srv.URL
	t.Cleanup(func() { TelegramAPI = old; srv.Close() })
}

func TestTelegramSend(t *testing.T) {
	var msg map[string]any
	var files []string
	fakeTelegram(t, func(method string, r *http.Request) string {
		switch method {
		case "sendMessage":
			_ = json.NewDecoder(r.Body).Decode(&msg)
		case "sendPhoto", "sendDocument":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
			}
			files = append(files, method+":"+r.FormValue("caption"))
		}
		return `{"ok":true,"result":{}}`
	})
	m := Message{Severity: "warning", Title: "Datensicherung unvollständig", Body: "3 von 4 <Servern> gesichert.", Host: "pk95",
		Details: &Details{Status: "warning", Facts: []Fact{{Label: "Dauer", Value: "12 Min."}}, RowsTitle: "Server",
			Rows:   []Row{{Kind: "VM", ID: "100", Name: "dc01", Status: "ok", Size: "32 GiB"}, {Kind: "VM", ID: "112", Name: "win", Status: "error", Info: "nicht gesichert"}},
			Log:    []string{"ERROR: VM 112 failed"},
			Images: []Image{{CID: "s1", Caption: "dc01 startet", Status: "ok"}}},
		Attachments: []Attachment{{Name: "s1.jpg", ContentType: "image/jpeg", Data: []byte("x"), ContentID: "s1"}, {Name: "bericht.html", ContentType: "text/html", Data: []byte("<p>")}}}
	s := Settings{TelegramEnabled: true, TelegramToken: testToken, TelegramChat: "-1001234567", TelegramFiles: true}
	if err := Send(context.Background(), s, m); err != nil {
		t.Fatal(err)
	}
	text, _ := msg["text"].(string)
	for _, want := range []string{"⚠️ <b>Datensicherung unvollständig</b>", "&lt;Servern&gt;", "<b>Dauer:</b> 12 Min.", "❌ VM 112 <b>win</b> – nicht gesichert", "<pre>ERROR: VM 112 failed</pre>"} {
		if !strings.Contains(text, want) {
			t.Errorf("text misses %q:\n%s", want, text)
		}
	}
	if msg["chat_id"] != "-1001234567" || msg["parse_mode"] != "HTML" || msg["reply_markup"] != nil {
		t.Fatalf("payload: %v", msg)
	}
	if strings.Join(files, "|") != "sendPhoto:✅ dc01 startet|sendDocument:bericht.html" {
		t.Fatalf("files: %v", files)
	}
}

func TestTelegramTextLimit(t *testing.T) {
	var rows []Row
	for i := 0; i < 300; i++ {
		rows = append(rows, Row{Kind: "VM", ID: "1", Name: strings.Repeat("n", 60), Status: "ok", Info: strings.Repeat("i", 80)})
	}
	m := Message{Severity: "info", Title: "x", Body: "y", Details: &Details{Rows: rows, Log: []string{strings.Repeat("log ", 2000)}}}
	if n := utf8.RuneCountInString(telegramText(m)); n > 4096 {
		t.Fatalf("text has %d characters", n)
	}
}

func TestTelegramChatsAndErrors(t *testing.T) {
	fakeTelegram(t, func(method string, r *http.Request) string {
		return `{"ok":true,"result":[
			{"update_id":1,"message":{"chat":{"id":4711,"type":"private","first_name":"Philipp","username":"pk"}}},
			{"update_id":2,"my_chat_member":{"chat":{"id":-100999,"type":"supergroup","title":"Kunde Müller"}}},
			{"update_id":3,"message":{"chat":{"id":4711,"type":"private","first_name":"Philipp","username":"pk"}}}]}`
	})
	chats, err := TelegramChats(context.Background(), testToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 2 || chats[0].ID != 4711 || chats[0].Title != "Philipp (@pk)" || chats[1].Title != "Kunde Müller" {
		t.Fatalf("chats: %+v", chats)
	}
	bad := "999999:" + strings.Repeat("x", 35)
	_, err = TelegramChats(context.Background(), bad)
	if err == nil || !strings.Contains(err.Error(), "Bot-Token ungültig") || strings.Contains(err.Error(), bad) {
		t.Fatalf("bad token: %v", err)
	}
	TelegramAPI = "http://127.0.0.1:1"
	err = Send(context.Background(), Settings{TelegramEnabled: true, TelegramToken: testToken, TelegramChat: "4711"}, Message{Title: "x"})
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("network error must not contain the token: %v", err)
	}
	if !ValidTelegramChat("@kanal_name") || ValidTelegramChat("abc") || ValidTelegramToken("123:abc") {
		t.Fatal("validation")
	}
}
