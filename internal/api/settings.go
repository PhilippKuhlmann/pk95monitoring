package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"pk95monitoring/internal/core"
	"pk95monitoring/internal/notify"
	"pk95monitoring/internal/store"
)

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, redactSettings(s.C.Settings()))
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "Anfrage zu groß.")
		return nil, false
	}
	return b, true
}

// putSettings saves the sections present in the body (the UI sends one per call).
func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	body, good := readBody(w, r)
	if !good {
		return
	}
	merged, err := mergeSettings(s.C.Settings(), body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, badJSON(err))
		return
	}
	errs, err := s.C.SaveSettings(merged)
	if fieldErrs(w, errs) {
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, redactSettings(s.C.Settings()))
}

// testNotify sends a test message with the unsaved form values ({notify: {…}}); keys and
// secrets that are missing or empty come from the stored settings.
func (s *Server) testNotify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Notify json.RawMessage `json:"notify"`
	}
	if !decode(w, r, &req) {
		return
	}
	n := s.C.Settings().Notify
	if len(req.Notify) > 0 {
		if err := json.Unmarshal(req.Notify, &n); err != nil {
			writeErr(w, http.StatusBadRequest, badJSON(err))
			return
		}
	}
	if !n.MailEnabled && !n.HookEnabled && !n.TelegramEnabled {
		writeErr(w, http.StatusBadRequest, "Kein Benachrichtigungsweg eingeschaltet.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	if err := s.C.TestNotify(ctx, n); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	ok(w)
}

// telegramChats reads the chats that wrote to the bot, to pick the chat id.
func (s *Server) telegramChats(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &req) {
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		token = s.C.Settings().Notify.TelegramToken
	}
	if !notify.ValidTelegramToken(token) {
		fieldErrs(w, map[string]string{"telegram_token": "Zuerst den Bot-Token von @BotFather eintragen."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	chats, err := notify.TelegramChats(ctx, token)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if chats == nil {
		chats = []notify.TelegramChat{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"chats": chats})
}

// ---- API tokens ----

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	list, err := s.C.St.APITokens()
	if err != nil {
		fail(w, err)
		return
	}
	if list == nil {
		list = []store.APIToken{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": list})
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len([]rune(req.Name)) > 64 {
		fieldErrs(w, map[string]string{"name": "Name mit 1 bis 64 Zeichen angeben."})
		return
	}
	plain, t, err := s.C.St.CreateAPIToken(req.Name)
	if err != nil {
		fail(w, err)
		return
	}
	s.C.Event("info", "System", "API-Token „"+t.Name+"“ erstellt", "Erstellt von "+currentUser(r).Username+".", nil, false)
	writeJSON(w, http.StatusOK, struct {
		store.APIToken
		Token string `json:"token"`
	}{t, plain})
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "Nicht gefunden.")
		return
	}
	if err := s.C.St.DeleteAPIToken(id); err != nil {
		fail(w, err)
		return
	}
	s.C.Event("info", "System", "API-Token gelöscht", "Gelöscht von "+currentUser(r).Username+".", nil, false)
	ok(w)
}

// ---- system ----

func (s *Server) system(w http.ResponseWriter, r *http.Request) {
	n, _ := s.C.St.SeriesCount()
	writeJSON(w, http.StatusOK, map[string]any{
		"version": s.C.Opt.Version, "instance": s.C.InstanceName(), "started": s.C.Started, "db_size": s.C.St.DBSize(),
		"devices": len(s.C.Devices()), "series": n, "data_dir": s.C.Opt.DataDir, "demo": s.C.Opt.Demo,
	})
}

// listNotifyRules lists the notification rules and which channels are switched on.
func (s *Server) listNotifyRules(w http.ResponseWriter, r *http.Request) {
	n := s.C.Settings().Notify
	writeJSON(w, http.StatusOK, map[string]any{"rules": s.C.NotifyRules(),
		"channels": map[string]any{"mail": n.MailEnabled, "telegram": n.TelegramEnabled, "hook": n.HookEnabled, "mail_to": n.MailTo}})
}

// saveNotifyRule creates (POST) or changes (PUT /{id}) a notification rule.
func (s *Server) saveNotifyRule(w http.ResponseWriter, r *http.Request) {
	var rule core.NotifyRule
	if !decode(w, r, &rule) {
		return
	}
	rule.ID = r.PathValue("id")
	v, errs, err := s.C.SaveNotifyRule(rule)
	if fieldErrs(w, errs) {
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// deleteNotifyRule removes a notification rule.
func (s *Server) deleteNotifyRule(w http.ResponseWriter, r *http.Request) {
	if err := s.C.DeleteNotifyRule(r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}
