package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newStore(t testing.TB) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func count(t testing.TB, s *Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMigrateFromEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != len(migrations) {
		t.Fatalf("user_version = %d, want %d", v, len(migrations))
	}
	for _, table := range []string{"settings", "objects", "users", "sessions", "events", "series", "samples",
		"rollups", "alerts", "statuses", "api_tokens", "agent_tokens"} {
		if n := count(t, s, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table); n != 1 {
			t.Errorf("table %s missing", table)
		}
	}
	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode = %q, %v", mode, err)
	}
	if err := s.SetSetting("x", 1); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Reopening keeps the data and does not migrate again.
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var x int
	if err := s.GetSetting("x", &x); err != nil || x != 1 {
		t.Fatalf("setting after reopen = %d, %v", x, err)
	}
	if s.DBSize() <= 0 {
		t.Error("DBSize must be positive")
	}
}

func TestSettingsAndObjects(t *testing.T) {
	s := newStore(t)
	type doc struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var missing doc
	if err := s.GetSetting("nope", &missing); err != nil || missing.ID != "" {
		t.Fatalf("missing setting: %+v %v", missing, err)
	}
	if err := s.Put("device", "b", doc{"b", "Beta"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("device", "a", doc{"a", "Alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("device", "b", doc{"b", "Beta 2"}); err != nil {
		t.Fatal(err)
	}
	var d doc
	if err := s.Get("device", "b", &d); err != nil || d.Name != "Beta 2" {
		t.Fatalf("Get = %+v %v", d, err)
	}
	all, err := List[doc](s, "device")
	if err != nil || len(all) != 2 || all[0].ID != "b" {
		t.Fatalf("List = %+v %v (want creation order)", all, err)
	}
	if err := s.Delete("device", "b"); err != nil {
		t.Fatal(err)
	}
	if err := s.Get("device", "b", &d); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get deleted = %v", err)
	}
}

func TestUsersSessionsEvents(t *testing.T) {
	s := newStore(t)
	id, err := s.CreateUser("Admin", "h1")
	if err != nil {
		t.Fatal(err)
	}
	if u, err := s.UserByName("admin"); err != nil || u.ID != id {
		t.Fatalf("UserByName case-insensitive: %+v %v", u, err)
	}
	if err := s.CreateSession("tok1", id, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession("tok2", id, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPassword(id, "h2", "tok1"); err != nil {
		t.Fatal(err)
	}
	if u, err := s.SessionUser("tok1"); err != nil || u.Hash != "h2" {
		t.Fatalf("kept session: %+v %v", u, err)
	}
	if _, err := s.SessionUser("tok2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other session must end: %v", err)
	}

	for i, sev := range []string{"info", "warning", "critical"} {
		if _, err := s.AddEvent(Event{TS: int64(100 + i), Severity: sev, Source: "test", Title: sev,
			Details: json.RawMessage(`{"n":1}`)}); err != nil {
			t.Fatal(err)
		}
	}
	ev, err := s.Events(context.Background(), "warning", 0, 10)
	if err != nil || len(ev) != 2 || ev[0].Severity != "critical" || string(ev[0].Details) != `{"n":1}` {
		t.Fatalf("Events = %+v %v", ev, err)
	}
	ev, _ = s.Events(context.Background(), "", ev[1].ID, 10)
	if len(ev) != 1 || ev[0].Severity != "info" {
		t.Fatalf("paging = %+v", ev)
	}
	if ev, _ := s.Events(context.Background(), "", 0, 0); len(ev) != 3 {
		t.Fatalf("limit 0 must use the default: %d events", len(ev))
	}
}
