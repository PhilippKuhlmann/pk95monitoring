// Package store keeps the monitoring state in SQLite: configuration objects as JSON documents,
// users and sessions, the event log, metric time series with rollups, alerts, device statuses
// and the API and agent tokens.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const dsnOptions = "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)" +
	"&_pragma=synchronous(NORMAL)&_txlock=immediate"

var migrations = []string{
	`
CREATE TABLE settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE objects (
	kind       TEXT    NOT NULL,
	id         TEXT    NOT NULL,
	data       TEXT    NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (kind, id)
);
CREATE TABLE users (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	username      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
	password_hash TEXT    NOT NULL,
	created_at    INTEGER NOT NULL
);
CREATE TABLE sessions (
	token_hash TEXT    PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL
);
CREATE TABLE events (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	ts       INTEGER NOT NULL,
	severity TEXT    NOT NULL,
	source   TEXT    NOT NULL,
	title    TEXT    NOT NULL,
	body     TEXT    NOT NULL DEFAULT '',
	details  TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX events_ts ON events (ts);

CREATE TABLE series (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	device_id  TEXT    NOT NULL,
	name       TEXT    NOT NULL,
	instance   TEXT    NOT NULL DEFAULT '',
	unit       TEXT    NOT NULL DEFAULT '',
	check_key  TEXT    NOT NULL DEFAULT '',
	last_ts    INTEGER NOT NULL DEFAULT 0,
	last_value REAL    NOT NULL DEFAULT 0,
	UNIQUE (device_id, name, instance)
);
CREATE INDEX series_name ON series (name);
CREATE TABLE samples (
	series_id INTEGER NOT NULL,
	ts        INTEGER NOT NULL,
	value     REAL    NOT NULL,
	PRIMARY KEY (series_id, ts)
) WITHOUT ROWID;
CREATE TABLE rollups (
	series_id INTEGER NOT NULL,
	res       INTEGER NOT NULL,
	ts        INTEGER NOT NULL,
	min       REAL    NOT NULL,
	max       REAL    NOT NULL,
	avg       REAL    NOT NULL,
	cnt       INTEGER NOT NULL,
	PRIMARY KEY (series_id, res, ts)
) WITHOUT ROWID;
CREATE TABLE alerts (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	device_id   TEXT    NOT NULL,
	device_name TEXT    NOT NULL DEFAULT '',
	check_key   TEXT    NOT NULL,
	check_name  TEXT    NOT NULL DEFAULT '',
	severity    TEXT    NOT NULL,
	message     TEXT    NOT NULL DEFAULT '',
	started     INTEGER NOT NULL,
	ended       INTEGER NOT NULL DEFAULT 0,
	acked_by    TEXT    NOT NULL DEFAULT '',
	acked_at    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX alerts_ended ON alerts (ended);
CREATE INDEX alerts_device ON alerts (device_id, id);
CREATE TABLE statuses (
	device_id TEXT PRIMARY KEY,
	data      TEXT NOT NULL
);
CREATE TABLE api_tokens (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT    NOT NULL,
	token_hash TEXT    NOT NULL UNIQUE,
	prefix     TEXT    NOT NULL,
	created    INTEGER NOT NULL,
	last_used  INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE agent_tokens (
	device_id  TEXT    PRIMARY KEY,
	token_hash TEXT    NOT NULL UNIQUE,
	created    INTEGER NOT NULL
);
`,
}

// ErrNotFound is returned when an object does not exist.
var ErrNotFound = errors.New("nicht gefunden")

// Store wraps the database.
type Store struct {
	db *sql.DB

	// wmu serializes writes to series, samples and rollups (SQLite has one writer anyway;
	// waiting here instead of in busy_timeout keeps large batches from failing).
	wmu    sync.Mutex
	series map[string]int64 // series IDs by seriesCacheKey, guarded by wmu

	// rollMu serializes Rollup calls; rmu guards the rollup watermarks (see rollup.go).
	rollMu   sync.Mutex
	rmu      sync.Mutex
	roll     rollState // progress of the rollup, persisted in settings
	done     rollState // watermarks of completed rollups, used by Query
	inflight int64     // start of the range the running Rollup works on (0 = none)
}

// Open opens (and migrates) the database file.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?"+dsnOptions)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	s := &Store{db: db, series: map[string]int64{}}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.GetSetting(rollKey, &s.roll); err != nil {
		db.Close()
		return nil, err
	}
	s.done = s.roll
	if d := s.roll.Dirty; d != 0 {
		// Rollups from d on are incomplete (e.g. a crash during Rollup): Query reads raw data there.
		s.done.Five = min(s.done.Five, floorTo(d, Res5m))
		s.done.Hour = min(s.done.Hour, floorTo(d, Res1h))
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// ---- settings ----

// GetSetting decodes the JSON value of key into v. Missing keys leave v unchanged.
func (s *Store) GetSetting(key string, v any) error {
	var raw string
	err := s.db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), v)
}

// SetSetting stores v as JSON under key.
func (s *Store) SetSetting(key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value", key, string(b))
	return err
}

// ---- objects ----

// Put inserts or replaces an object.
func (s *Store) Put(kind, id string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	now := time.Now().UnixNano()
	_, err = s.db.Exec(`INSERT INTO objects (kind, id, data, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (kind, id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`, kind, id, string(b), now, now)
	return err
}

// Get loads one object.
func (s *Store) Get(kind, id string, v any) error {
	var raw string
	err := s.db.QueryRow("SELECT data FROM objects WHERE kind = ? AND id = ?", kind, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), v)
}

// Delete removes one object.
func (s *Store) Delete(kind, id string) error {
	_, err := s.db.Exec("DELETE FROM objects WHERE kind = ? AND id = ?", kind, id)
	return err
}

// List loads all objects of a kind in creation order.
func List[T any](s *Store, kind string) ([]T, error) {
	rows, err := s.db.Query("SELECT data FROM objects WHERE kind = ? ORDER BY created_at, id", kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var v T
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---- users and sessions ----

// User is an account of the web UI.
type User struct {
	ID       int64
	Username string
	Hash     string
}

// UserCount returns the number of accounts.
func (s *Store) UserCount() (int, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&n)
	return n, err
}

// CreateUser adds an account.
func (s *Store) CreateUser(username, hash string) (int64, error) {
	res, err := s.db.Exec("INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?)", username, hash, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UserByName finds an account by name.
func (s *Store) UserByName(username string) (User, error) {
	var u User
	err := s.db.QueryRow("SELECT id, username, password_hash FROM users WHERE username = ?", username).Scan(&u.ID, &u.Username, &u.Hash)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// UserByID finds an account by id.
func (s *Store) UserByID(id int64) (User, error) {
	var u User
	err := s.db.QueryRow("SELECT id, username, password_hash FROM users WHERE id = ?", id).Scan(&u.ID, &u.Username, &u.Hash)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// SetPassword replaces the password hash and ends all other sessions of the user.
func (s *Store) SetPassword(userID int64, hash, keepToken string) error {
	if _, err := s.db.Exec("UPDATE users SET password_hash = ? WHERE id = ?", hash, userID); err != nil {
		return err
	}
	_, err := s.db.Exec("DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?", userID, hashToken(keepToken))
	return err
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// CreateSession stores a new session token.
func (s *Store) CreateSession(token string, userID int64, ttl time.Duration) error {
	now := time.Now()
	_, err := s.db.Exec("INSERT INTO sessions (token_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)",
		hashToken(token), userID, now.Unix(), now.Add(ttl).Unix())
	return err
}

// SessionUser returns the user of a valid session.
func (s *Store) SessionUser(token string) (User, error) {
	var id int64
	err := s.db.QueryRow("SELECT user_id FROM sessions WHERE token_hash = ? AND expires_at > ?", hashToken(token), time.Now().Unix()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return s.UserByID(id)
}

// DeleteSession ends a session.
func (s *Store) DeleteSession(token string) error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE token_hash = ?", hashToken(token))
	return err
}

// PruneSessions removes expired sessions.
func (s *Store) PruneSessions() error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE expires_at <= ?", time.Now().Unix())
	return err
}

// ---- events ----

// Event is an entry of the event log.
type Event struct {
	ID       int64           `json:"id"`
	TS       int64           `json:"ts"`
	Severity string          `json:"severity"` // info, warning, critical
	Source   string          `json:"source"`
	Title    string          `json:"title"`
	Body     string          `json:"body"`
	Details  json.RawMessage `json:"details,omitempty"`
}

// AddEvent appends to the event log.
func (s *Store) AddEvent(e Event) (Event, error) {
	if e.TS == 0 {
		e.TS = time.Now().Unix()
	}
	res, err := s.db.Exec("INSERT INTO events (ts, severity, source, title, body, details) VALUES (?, ?, ?, ?, ?, ?)", e.TS, e.Severity, e.Source, e.Title, e.Body, string(e.Details))
	if err != nil {
		return e, err
	}
	e.ID, _ = res.LastInsertId()
	return e, nil
}

// Events lists the newest events, optionally only from a minimum severity, before an id (paging).
// limit <= 0 means 100.
func (s *Store) Events(ctx context.Context, minSeverity string, before int64, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 100
	}
	q := "SELECT id, ts, severity, source, title, body, details FROM events WHERE 1=1"
	args := []any{}
	switch minSeverity {
	case "warning":
		q += " AND severity IN ('warning','critical')"
	case "critical":
		q += " AND severity = 'critical'"
	}
	if before > 0 {
		q += " AND id < ?"
		args = append(args, before)
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var details string
		if err := rows.Scan(&e.ID, &e.TS, &e.Severity, &e.Source, &e.Title, &e.Body, &details); err != nil {
			return nil, err
		}
		if details != "" {
			e.Details = json.RawMessage(details)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneEvents keeps the event log at the given age.
func (s *Store) PruneEvents(maxAge time.Duration) error {
	_, err := s.db.Exec("DELETE FROM events WHERE ts < ?", time.Now().Add(-maxAge).Unix())
	return err
}
