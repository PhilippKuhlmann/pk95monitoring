package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// Tokens are stored as SHA-256 hashes only. A lookup by hash does not leak timing information
// about the plain token.

// APITokenPrefix starts every API token of the external API.
const APITokenPrefix = "pk95_"

// APIToken is a bearer token of the external API (without the secret).
type APIToken struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Prefix   string `json:"prefix"` // first 12 characters, to recognize the token
	Created  int64  `json:"created"`
	LastUsed int64  `json:"last_used"`
}

// CreateAPIToken creates a token and returns its plain value (shown only once).
func (s *Store) CreateAPIToken(name string) (string, APIToken, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", APIToken{}, err
	}
	plain := APITokenPrefix + hex.EncodeToString(b)
	t := APIToken{Name: name, Prefix: plain[:12], Created: time.Now().Unix()}
	res, err := s.db.Exec("INSERT INTO api_tokens (name, token_hash, prefix, created, last_used) VALUES (?, ?, ?, ?, 0)",
		t.Name, hashToken(plain), t.Prefix, t.Created)
	if err != nil {
		return "", APIToken{}, err
	}
	t.ID, err = res.LastInsertId()
	return plain, t, err
}

// APITokens lists all API tokens in creation order.
func (s *Store) APITokens() ([]APIToken, error) {
	rows, err := s.db.Query("SELECT id, name, prefix, created, last_used FROM api_tokens ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &t.Created, &t.LastUsed); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteAPIToken revokes a token.
func (s *Store) DeleteAPIToken(id int64) error {
	return affected(s.db.Exec("DELETE FROM api_tokens WHERE id = ?", id))
}

// CheckAPIToken returns the token for a plain value (ErrNotFound when unknown) and records its
// use (at most once per minute).
func (s *Store) CheckAPIToken(plain string) (APIToken, error) {
	var t APIToken
	if !strings.HasPrefix(plain, APITokenPrefix) {
		return t, ErrNotFound
	}
	err := s.db.QueryRow("SELECT id, name, prefix, created, last_used FROM api_tokens WHERE token_hash = ?", hashToken(plain)).
		Scan(&t.ID, &t.Name, &t.Prefix, &t.Created, &t.LastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	// Recording the use is bookkeeping: a busy database must not fail the authentication.
	if now := time.Now().Unix(); now-t.LastUsed >= 60 {
		if _, err := s.db.Exec("UPDATE api_tokens SET last_used = ? WHERE id = ?", now, t.ID); err == nil {
			t.LastUsed = now
		}
	}
	return t, nil
}

// ---- agent tokens ----

// SetAgentToken sets the token of an agent device, replacing the previous one.
func (s *Store) SetAgentToken(deviceID, plain string) error {
	if plain == "" {
		return errors.New("leerer Agent-Token")
	}
	_, err := s.db.Exec("INSERT OR REPLACE INTO agent_tokens (device_id, token_hash, created) VALUES (?, ?, ?)",
		deviceID, hashToken(plain), time.Now().Unix())
	return err
}

// AgentDevice returns the device id of an agent token (ErrNotFound when unknown).
func (s *Store) AgentDevice(plain string) (string, error) {
	if plain == "" {
		return "", ErrNotFound
	}
	var id string
	err := s.db.QueryRow("SELECT device_id FROM agent_tokens WHERE token_hash = ?", hashToken(plain)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

// DeleteAgentToken revokes the token of an agent device.
func (s *Store) DeleteAgentToken(deviceID string) error {
	_, err := s.db.Exec("DELETE FROM agent_tokens WHERE device_id = ?", deviceID)
	return err
}
