package store

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

func TestAPITokens(t *testing.T) {
	s := newStore(t)
	plain, tok, err := s.CreateAPIToken("Zentrale")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^pk95_[0-9a-f]{40}$`).MatchString(plain) {
		t.Fatalf("token format: %q", plain)
	}
	if tok.Prefix != plain[:12] || tok.Name != "Zentrale" || tok.ID == 0 || tok.Created == 0 || tok.LastUsed != 0 {
		t.Fatalf("token = %+v", tok)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM api_tokens WHERE token_hash = ?", plain); n != 0 {
		t.Fatal("plain token stored")
	}
	plain2, _, _ := s.CreateAPIToken("Zweiter")
	if plain2 == plain {
		t.Fatal("tokens must differ")
	}

	got, err := s.CheckAPIToken(plain)
	if err != nil || got.ID != tok.ID || got.LastUsed == 0 {
		t.Fatalf("CheckAPIToken = %+v %v", got, err)
	}
	// last_used is written at most once per minute.
	if _, err := s.db.Exec("UPDATE api_tokens SET last_used = last_used - 30 WHERE id = ?", tok.ID); err != nil {
		t.Fatal(err)
	}
	again, _ := s.CheckAPIToken(plain)
	if again.LastUsed != got.LastUsed-30 {
		t.Fatalf("last_used rewritten within a minute: %d vs %d", again.LastUsed, got.LastUsed)
	}

	for _, bad := range []string{"", "pk95_", plain + "x", strings.ToUpper(plain), "Bearer " + plain} {
		if _, err := s.CheckAPIToken(bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("CheckAPIToken(%q) = %v", bad, err)
		}
	}

	list, err := s.APITokens()
	if err != nil || len(list) != 2 || list[0].Name != "Zentrale" {
		t.Fatalf("APITokens = %+v %v", list, err)
	}
	if err := s.DeleteAPIToken(tok.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAPIToken(tok.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
	if _, err := s.CheckAPIToken(plain); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted token still valid: %v", err)
	}
}

func TestAgentTokens(t *testing.T) {
	s := newStore(t)
	if err := s.SetAgentToken("d1", "first"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentToken("d2", "other"); err != nil {
		t.Fatal(err)
	}
	if id, err := s.AgentDevice("first"); err != nil || id != "d1" {
		t.Fatalf("AgentDevice = %q %v", id, err)
	}
	// Replacing invalidates the old token.
	if err := s.SetAgentToken("d1", "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AgentDevice("first"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old token = %v", err)
	}
	if id, _ := s.AgentDevice("second"); id != "d1" {
		t.Fatalf("new token = %q", id)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM agent_tokens WHERE token_hash = 'second'"); n != 0 {
		t.Fatal("plain token stored")
	}
	if err := s.SetAgentToken("d3", ""); err == nil {
		t.Fatal("empty token accepted")
	}
	if _, err := s.AgentDevice(""); !errors.Is(err, ErrNotFound) {
		t.Fatal("empty token must not match")
	}
	if err := s.DeleteAgentToken("d1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AgentDevice("second"); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted token still valid")
	}
	if id, _ := s.AgentDevice("other"); id != "d2" {
		t.Fatal("other device affected")
	}
}
