package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"pk95monitoring/internal/model"
)

// ErrUnauthorized: the server rejected the token (agent deleted on the server, or key wrong).
var ErrUnauthorized = errors.New("token ungültig")

// StatusError is a non-2xx answer of the server.
type StatusError struct {
	Code int
	Msg  string
}

func (e *StatusError) Error() string {
	if e.Msg != "" {
		return fmt.Sprintf("HTTP %d: %s", e.Code, e.Msg)
	}
	return fmt.Sprintf("HTTP %d", e.Code)
}

// Permanent reports whether a retry cannot succeed: 4xx except 401, 408 and 429.
func (e *StatusError) Permanent() bool {
	return e.Code >= 400 && e.Code < 500 &&
		e.Code != http.StatusUnauthorized && e.Code != http.StatusRequestTimeout && e.Code != http.StatusTooManyRequests
}

// IsPermanent reports whether err is a StatusError that will not go away by retrying.
func IsPermanent(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Permanent()
}

// NormalizeFingerprint turns "AB:CD:…" or "abcd…" into 64 lower-case hex characters.
// An empty input stays empty (no pinning).
func NormalizeFingerprint(fp string) (string, error) {
	r := strings.NewReplacer(":", "", " ", "", "-", "")
	s := strings.ToLower(r.Replace(strings.TrimSpace(fp)))
	if s == "" {
		return "", nil
	}
	if len(s) != 64 {
		return "", fmt.Errorf("Fingerprint muss ein SHA-256-Wert sein (64 Hex-Zeichen), nicht %d", len(s))
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", errors.New("Fingerprint enthält ungültige Zeichen")
	}
	return s, nil
}

// TLSConfig returns the client TLS settings: certificate pinning when fingerprint is set,
// no verification at all with insecure, otherwise the system CAs.
func TLSConfig(fingerprint string, insecure bool) (*tls.Config, error) {
	fp, err := NormalizeFingerprint(fingerprint)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	switch {
	case fp != "":
		want, _ := hex.DecodeString(fp)
		// The chain is not verified (self-signed certificate); the pinned leaf is the trust anchor.
		cfg.InsecureSkipVerify = true
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("Server sendet kein Zertifikat")
			}
			got := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(got[:], want) != 1 {
				return fmt.Errorf("Zertifikat des Servers passt nicht zum hinterlegten Fingerprint (ist %s)", hex.EncodeToString(got[:]))
			}
			return nil
		}
	case insecure:
		cfg.InsecureSkipVerify = true
	}
	return cfg, nil
}

// NewHTTPClient builds the HTTP client used for all server requests.
func NewHTTPClient(fingerprint string, insecure bool, timeout time.Duration) (*http.Client, error) {
	tc, err := TLSConfig(fingerprint, insecure)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       tc,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          2,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{Transport: tr, Timeout: timeout}, nil
}

// Client talks to the monitoring server.
type Client struct {
	server string
	token  string
	http   *http.Client
	// dl has no overall timeout: binaries are several MB; the context limits downloads.
	dl *http.Client
}

// NewClient creates a client for server with the given bearer token.
func NewClient(server, token, fingerprint string, insecure bool) (*Client, error) {
	hc, err := NewHTTPClient(fingerprint, insecure, 30*time.Second)
	if err != nil {
		return nil, err
	}
	dl := *hc
	dl.Timeout = 0
	return &Client{server: strings.TrimRight(server, "/"), token: token, http: hc, dl: &dl}, nil
}

func (c *Client) do(ctx context.Context, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.server+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "pk95agent/"+Version)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%w (HTTP 401)", ErrUnauthorized)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		return &StatusError{Code: resp.StatusCode, Msg: e.Error}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("ungültige Antwort des Servers: %w", err)
		}
	}
	return nil
}

// Report sends one report and returns the server's configuration for the agent.
func (c *Client) Report(ctx context.Context, rep model.AgentReport) (model.AgentReply, error) {
	var reply model.AgentReply
	err := c.do(ctx, "/agent/v1/report", rep, &reply)
	return reply, err
}

// Download fetches path (a path on the server or an absolute URL) into w, reading at most
// limit bytes. The token is only sent to the server's own origin.
func (c *Client) Download(ctx context.Context, path string, w io.Writer, limit int64) (int64, error) {
	target := path
	if !strings.HasPrefix(path, "https://") && !strings.HasPrefix(path, "http://") {
		target = c.server + "/" + strings.TrimLeft(path, "/")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "pk95agent/"+Version)
	if c.token != "" && sameOrigin(c.server, req.URL) {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.dl.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, &StatusError{Code: resp.StatusCode}
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err == nil && n > limit {
		err = fmt.Errorf("Download größer als %d Byte", limit)
	}
	return n, err
}

// sameOrigin reports whether u has the scheme and host of the server URL.
func sameOrigin(server string, u *url.URL) bool {
	s, err := url.Parse(server)
	return err == nil && strings.EqualFold(s.Scheme, u.Scheme) && strings.EqualFold(s.Host, u.Host)
}

// Register enrolls this host with the enrollment key and returns its device ID and token.
func Register(ctx context.Context, server, key, fingerprint string, insecure bool) (model.AgentRegistered, error) {
	return RegisterDevice(ctx, server, key, fingerprint, insecure, "", "")
}

// RegisterDevice is Register that also attaches the agent to the existing device deviceID
// (e.g. a Proxmox cluster) and, optionally, its part node (e.g. the Proxmox node name).
// Empty deviceID = a standalone device.
func RegisterDevice(ctx context.Context, server, key, fingerprint string, insecure bool, deviceID, node string) (model.AgentRegistered, error) {
	c, err := NewClient(server, "", fingerprint, insecure)
	if err != nil {
		return model.AgentRegistered{}, err
	}
	h := HostFacts(ctx)
	req := model.AgentRegister{
		Key: key, AgentID: MachineID(), Hostname: h.Hostname, OS: h.OS, Platform: h.Platform,
		PlatformVersion: h.PlatformVersion, Kernel: h.Kernel, Arch: h.Arch, Version: Version, IPs: h.IPs,
		DeviceID: strings.TrimSpace(deviceID), Node: strings.TrimSpace(node),
	}
	var res model.AgentRegistered
	if err := c.do(ctx, "/agent/v1/register", req, &res); err != nil {
		if errors.Is(err, ErrUnauthorized) {
			return res, errors.New("Registrierungsschlüssel wurde abgelehnt")
		}
		return res, err
	}
	if res.Token == "" || res.DeviceID == "" {
		return res, errors.New("Server hat keinen Token geliefert")
	}
	return res, nil
}
