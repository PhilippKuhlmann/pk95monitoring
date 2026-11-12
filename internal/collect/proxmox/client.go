// Package proxmox polls Proxmox VE over its REST API (api2/json) with a read-only API token
// and turns cluster, nodes, guests, storages, disks, updates and backups into checks.
// The TLS certificate is pinned by its SHA-256 fingerprint (PVE uses a self-signed
// certificate by default); without a fingerprint the system CAs are used.
package proxmox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"pk95monitoring/internal/model"
)

// DefaultPort is the port of the Proxmox VE API.
const DefaultPort = 8006

// requestTimeout bounds one API call (the caller's context bounds the whole poll).
const requestTimeout = 20 * time.Second

// APIError is a non-2xx answer of the API.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

// isStatus reports whether err is an API error with that HTTP status.
func isStatus(err error, status int) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == status
}

// fingerprintError is returned when the server certificate does not match the pinned one.
type fingerprintError struct{ got string }

func (e *fingerprintError) Error() string {
	return "Fingerabdruck stimmt nicht (Server meldet " + FormatFingerprint(e.got) + ")"
}

// NormalizeFingerprint makes "aa:BB:..." / "aabb..." comparable.
func NormalizeFingerprint(f string) string {
	f = strings.ToLower(strings.TrimSpace(f))
	f = strings.ReplaceAll(f, ":", "")
	return strings.ReplaceAll(f, " ", "")
}

// FormatFingerprint renders a hex fingerprint as AA:BB:… (the form PVE shows).
func FormatFingerprint(f string) string {
	f = strings.ToUpper(NormalizeFingerprint(f))
	parts := make([]string, 0, len(f)/2)
	for i := 0; i+2 <= len(f); i += 2 {
		parts = append(parts, f[i:i+2])
	}
	return strings.Join(parts, ":")
}

func certFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return fmt.Sprintf("%x", sum[:])
}

func port(p int) int {
	if p <= 0 {
		return DefaultPort
	}
	return p
}

// Probe connects to host:port (0 = 8006) and returns the SHA-256 fingerprint of the leaf
// certificate as AA:BB:… (trust on first use: the UI shows it for confirmation).
func Probe(ctx context.Context, host string, p int) (string, error) {
	p = port(p)
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 8 * time.Second}, Config: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // only reads the certificate
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(p)))
	if err != nil {
		return "", fmt.Errorf("Keine Verbindung zu %s:%d: %w", host, p, err)
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", errors.New("der Server hat kein Zertifikat geschickt")
	}
	return FormatFingerprint(certFingerprint(certs[0].Raw)), nil
}

func newTransport(fingerprint string) *http.Transport {
	tr := &http.Transport{
		Proxy:               nil,
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConnsPerHost: 4,
		DialContext:         (&net.Dialer{Timeout: 8 * time.Second}).DialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
	}
	if want := NormalizeFingerprint(fingerprint); want != "" {
		tr.TLSClientConfig.InsecureSkipVerify = true //nolint:gosec // verified below by pinning
		tr.TLSClientConfig.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("kein Zertifikat")
			}
			if got := certFingerprint(raw[0]); got != want {
				return &fingerprintError{got: got}
			}
			return nil
		}
	}
	return tr
}

// client is a connection to one PVE API.
type client struct {
	base string // https://host:port/api2/json
	auth string // value of the Authorization header
	http *http.Client
	host string
	port int
	sig  string // connection parameters, to detect configuration changes
}

func newClient(host string, cfg model.ProxmoxConfig) *client {
	p := port(cfg.Port)
	return &client{
		base: "https://" + net.JoinHostPort(host, strconv.Itoa(p)) + "/api2/json",
		auth: "PVEAPIToken=" + strings.TrimSpace(cfg.TokenID) + "=" + strings.TrimSpace(cfg.Secret),
		http: &http.Client{Transport: newTransport(cfg.Fingerprint), Timeout: requestTimeout},
		host: host,
		port: p,
		sig:  signature(host, cfg),
	}
}

func signature(host string, cfg model.ProxmoxConfig) string {
	return strings.Join([]string{host, strconv.Itoa(port(cfg.Port)), cfg.TokenID, cfg.Secret, NormalizeFingerprint(cfg.Fingerprint)}, "\x00")
}

func (c *client) close() { c.http.CloseIdleConnections() }

// get calls GET path and decodes the "data" member of the answer into out.
func (c *client) get(ctx context.Context, path string, params url.Values, out any) error {
	u := c.base + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.auth)
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return &connError{addr: net.JoinHostPort(c.host, strconv.Itoa(c.port)), err: err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return &connError{addr: net.JoinHostPort(c.host, strconv.Itoa(c.port)), err: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{Status: resp.StatusCode, Message: apiMessage(resp, raw)}
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("unerwartete Antwort von %s: %w", c.host, err)
	}
	if len(env.Data) == 0 || bytes.Equal(env.Data, []byte("null")) {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("unerwartete Antwort von %s%s: %w", c.host, path, err)
	}
	return nil
}

// connError is a transport failure (DNS, TCP, TLS, timeout).
type connError struct {
	addr string
	err  error
}

func (e *connError) Error() string {
	var fe *fingerprintError
	if errors.As(e.err, &fe) {
		return fe.Error()
	}
	return "Keine Verbindung zu " + e.addr + ": " + e.err.Error()
}

func (e *connError) Unwrap() error { return e.err }

func apiMessage(resp *http.Response, raw []byte) string {
	var env struct {
		Message string            `json:"message"`
		Errors  map[string]string `json:"errors"`
	}
	msg := ""
	if json.Unmarshal(raw, &env) == nil {
		msg = strings.TrimSpace(env.Message)
		for k, v := range env.Errors {
			msg = strings.TrimSpace(msg + " " + k + ": " + strings.TrimSpace(v))
		}
	}
	if msg == "" {
		// PVE puts the reason into the status line.
		if _, reason, ok := strings.Cut(resp.Status, " "); ok {
			msg = strings.TrimSpace(reason)
		}
	}
	if msg == "" {
		msg = strings.TrimSpace(string(raw))
	}
	return msg
}

// friendly turns a failure of a basic call (version, resources) into a German message.
func friendly(err error) error {
	var fe *fingerprintError
	var ue x509.UnknownAuthorityError
	var ce *tls.CertificateVerificationError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &fe):
		return fe
	case errors.As(err, &ue), errors.As(err, &ce):
		return errors.New("Zertifikat nicht vertrauenswürdig – Fingerabdruck abrufen und hinterlegen")
	case isStatus(err, http.StatusUnauthorized):
		return errors.New("Token ungültig oder ohne Rechte (Anmeldung abgelehnt)")
	case isStatus(err, http.StatusForbidden):
		return fmt.Errorf("Token ungültig oder ohne Rechte (%v)", err)
	}
	return err
}

// permMessage renders a failed optional call as a check message; 403 becomes
// "Keine Berechtigung (<hint>)".
func permMessage(err error, hint string) string {
	if isStatus(err, http.StatusForbidden) {
		return "Keine Berechtigung (" + hint + ")"
	}
	return "Abfrage fehlgeschlagen: " + err.Error()
}

// num decodes JSON numbers that PVE sometimes sends as strings ("0.52", "N/A").
type num float64

func (n *num) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		f = 0 // "", null, "N/A"
	}
	*n = num(f)
	return nil
}

// optNum is a number that may be missing or "N/A".
type optNum struct {
	V  float64
	OK bool
}

func (n *optNum) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	*n = optNum{V: f, OK: err == nil}
	return nil
}
