package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"pk95monitoring/internal/model"
)

// ErrRestart: a new binary is in place; the agent must stop and start it (RestartSelf).
var ErrRestart = errors.New("Neustart nach Update")

const (
	maxBinarySize   = 256 << 20
	updateBackoff   = 6 * time.Hour
	downloadTimeout = 10 * time.Minute
)

// Updater replaces the running binary with the version the server offers.
type Updater struct {
	Client  *Client
	Exe     string // path of the running binary
	Version string // running version
	Log     *slog.Logger
	// Check runs the downloaded binary before it replaces the running one
	// (default: "<new> version" must print the expected version).
	Check func(path, version string) error
	// Backoff is the time before a failed version is tried again (default 6 h).
	Backoff time.Duration
	Now     func() time.Time

	mu     sync.Mutex
	failed map[string]time.Time
}

// NewUpdater prepares updates of the running executable and removes the binary a previous
// update left behind.
func NewUpdater(c *Client, log *slog.Logger) (*Updater, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	removeOldBinary(exe)
	return &Updater{Client: c, Exe: exe, Version: Version, Log: log}, nil
}

func (u *Updater) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

// Apply installs the offered version. It returns ErrRestart when the binary was replaced,
// nil when there is nothing to do (same version, or a recent failure of that version) and
// another error when the update failed (the running binary is untouched then).
func (u *Updater) Apply(ctx context.Context, upd model.AgentUpdate) error {
	if upd.Version == "" || upd.Version == u.Version || upd.URL == "" {
		return nil
	}
	backoff := u.Backoff
	if backoff <= 0 {
		backoff = updateBackoff
	}
	u.mu.Lock()
	at, failed := u.failed[upd.Version]
	u.mu.Unlock()
	if failed && u.now().Sub(at) < backoff {
		return nil
	}
	u.log().Info("installing update", "from", u.Version, "to", upd.Version)
	if err := u.install(ctx, upd); err != nil {
		u.mu.Lock()
		if u.failed == nil {
			u.failed = map[string]time.Time{}
		}
		u.failed[upd.Version] = u.now()
		u.mu.Unlock()
		u.log().Error("update failed, old version keeps running", "version", upd.Version, "err", err)
		return err
	}
	u.log().Info("update installed, restarting agent", "version", upd.Version)
	return ErrRestart
}

func (u *Updater) log() *slog.Logger {
	if u.Log != nil {
		return u.Log
	}
	return slog.Default()
}

func (u *Updater) install(ctx context.Context, upd model.AgentUpdate) error {
	want, err := NormalizeFingerprint(upd.SHA256) // same format: 64 hex characters
	if err != nil || want == "" {
		return errors.New("Update ohne gültige SHA-256-Prüfsumme")
	}
	pattern := ".pk95agent-update-*"
	if runtime.GOOS == "windows" {
		pattern += ".exe"
	}
	// Same directory as the binary, so the final rename is atomic and never crosses devices.
	f, err := os.CreateTemp(filepath.Dir(u.Exe), pattern)
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp)
		}
	}()
	h := sha256.New()
	// The download client has no overall timeout; a stalled transfer must not stop the reports.
	dctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	_, err = u.Client.Download(dctx, upd.URL, io.MultiWriter(f, h), maxBinarySize)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("Download: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("Prüfsumme stimmt nicht (erwartet %s, erhalten %s)", want, got)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	check := u.Check
	if check == nil {
		check = runVersion
	}
	if err := check(tmp, upd.Version); err != nil {
		return err
	}
	if err := replaceBinary(tmp, u.Exe); err != nil {
		return err
	}
	ok = true
	return nil
}

// runVersion starts the new binary with "version". It catches a wrong architecture or a
// broken file before the running binary is touched.
func runVersion(path, version string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("neues Programm startet nicht: %w", err)
	}
	return checkVersionOutput(string(out), version)
}

// checkVersionOutput verifies the output of "pk95agent version" ("pk95agent <version>
// <os>/<arch>"): exactly the offered version for this platform. A substring match would
// accept 1.2.10 for 1.2.1 and then update again on every report.
func checkVersionOutput(out, version string) error {
	f := strings.Fields(out)
	if len(f) < 3 || f[0] != "pk95agent" || f[1] != version || f[2] != runtime.GOOS+"/"+runtime.GOARCH {
		return fmt.Errorf("neues Programm meldet %q statt pk95agent %s %s/%s",
			truncate(out, 200), version, runtime.GOOS, runtime.GOARCH)
	}
	return nil
}
