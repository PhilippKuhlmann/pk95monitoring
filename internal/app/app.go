// Package app wires store, core, API and web UI together and runs the HTTP(S) servers.
package app

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"pk95monitoring/internal/agentdist"
	"pk95monitoring/internal/api"
	"pk95monitoring/internal/core"
	"pk95monitoring/internal/fake"
	"pk95monitoring/internal/store"
	"pk95monitoring/web"
)

// Config is the runtime configuration (from the environment, see DESIGN.md).
type Config struct {
	Version      string
	Listen       string // HTTPS listener (HTTP with NoTLS)
	HTTPRedirect string // plain HTTP listener that redirects to HTTPS ("" = off)
	DataDir      string
	AgentDir     string // agent binaries
	PublicURL    string // URL agents use to reach the server ("" = from the request)
	Instance     string // instance name (wins over the setting)
	Demo         bool   // simulated devices
	NoTLS        bool   // plain HTTP behind a reverse proxy
}

// Run starts everything and blocks until ctx is done.
func Run(ctx context.Context, cfg Config) error {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "pk95monitoring.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	c, err := core.New(st, core.Options{Version: cfg.Version, DataDir: cfg.DataDir, AgentDir: cfg.AgentDir,
		PublicURL: cfg.PublicURL, Instance: cfg.Instance, Demo: cfg.Demo})
	if err != nil {
		return err
	}
	if c.Agents == nil {
		c.Agents = agentdist.NewCatalog(cfg.AgentDir, cfg.Version)
	}
	if cfg.Demo {
		stop, err := fake.Setup(c)
		if err != nil {
			return fmt.Errorf("demo: %w", err)
		}
		defer stop()
	}
	if err := ensureSetupCode(st, cfg.DataDir); err != nil {
		return err
	}

	opt := api.Options{Secure: !cfg.NoTLS}
	var tlsCfg *tls.Config
	if !cfg.NoTLS {
		cert, err := loadOrCreateCert(cfg.DataDir, publicHost(cfg.PublicURL))
		if err != nil {
			return fmt.Errorf("certificate: %w", err)
		}
		info := certInfo(cert)
		opt.Fingerprint, opt.PubKeyPin, opt.Pinned = info.fingerprint, info.pin, !info.trusted
		tlsCfg = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		log.Printf("certificate SHA-256 fingerprint %s (trusted by system CAs: %v)", info.fingerprint, info.trusted)
	}

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	coreDone := make(chan struct{})
	go func() {
		defer close(coreDone)
		c.Run(runCtx)
	}()

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           api.New(c, web.FS, opt),
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
		// A client that sends its body (or reads the answer) byte by byte must not hold a
		// connection forever. Generous: agent reports over slow links, connection tests
		// (up to 90 s) and agent downloads (~10 MB).
		ReadTimeout:    2 * time.Minute,
		WriteTimeout:   10 * time.Minute,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 64 << 10,
	}
	errc := make(chan error, 1)
	go func() {
		if cfg.NoTLS {
			errc <- srv.ListenAndServe()
		} else {
			errc <- srv.ListenAndServeTLS("", "")
		}
	}()
	if cfg.HTTPRedirect != "" {
		if cfg.NoTLS {
			log.Printf("PK95MON_HTTP_REDIRECT ignored: TLS is off")
		} else {
			redir := &http.Server{Addr: cfg.HTTPRedirect, Handler: redirectHandler(cfg.Listen, srv.Handler),
				ReadHeaderTimeout: srv.ReadHeaderTimeout, ReadTimeout: srv.ReadTimeout, WriteTimeout: srv.WriteTimeout,
				IdleTimeout: srv.IdleTimeout, MaxHeaderBytes: srv.MaxHeaderBytes}
			go func() {
				if err := redir.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Printf("http redirect: %v", err)
				}
			}()
			defer redir.Close()
		}
	}
	scheme := "https"
	if cfg.NoTLS {
		scheme = "http"
	}
	log.Printf("PK95 Monitoring %s listening on %s (%s, data: %s, demo: %v)", cfg.Version, cfg.Listen, scheme, cfg.DataDir, cfg.Demo)

	var runErr error
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			runErr = err
		}
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil && runErr == nil {
		runErr = err
	}
	cancelRun()
	select {
	case <-coreDone:
	case <-time.After(15 * time.Second):
		log.Printf("core did not stop within 15 s")
	}
	return runErr
}

// redirectHandler sends plain HTTP requests to the HTTPS listener. A reverse proxy that
// already speaks HTTPS to the browser may talk plain HTTP to us: it is served instead of
// redirected (which would loop).
func redirectHandler(listen string, handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proto := strings.ToLower(r.Header.Get("X-Forwarded-Proto"))
		if proto == "https" || (proto == "" && r.Header.Get("X-Forwarded-For") != "") {
			handler.ServeHTTP(w, r)
			return
		}
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
			host = "[" + host + "]"
		}
		_, port, _ := net.SplitHostPort(listen)
		target := "https://" + host
		if port != "" && port != "443" {
			target += ":" + port
		}
		http.Redirect(w, r, target+r.URL.RequestURI(), http.StatusMovedPermanently)
	})
}

// ensureSetupCode writes a one-time code while no account exists and logs it.
func ensureSetupCode(st *store.Store, dataDir string) error {
	path := api.SetupCodePath(dataDir)
	n, err := st.UserCount()
	if err != nil {
		return err
	}
	if n > 0 {
		_ = os.Remove(path)
		return nil
	}
	code := ""
	if b, err := os.ReadFile(path); err == nil {
		code = strings.TrimSpace(string(b))
	}
	if code == "" {
		code = newSetupCode()
		if err := os.WriteFile(path, []byte(code+"\n"), 0o600); err != nil {
			return err
		}
	}
	// Shown to the administrator in "docker logs" (German, like the UI).
	log.Printf("setup code: %s (also in %s)", code, path)
	return nil
}

func newSetupCode() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	buf := make([]byte, 8)
	for i := range buf {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		buf[i] = alphabet[n.Int64()]
	}
	return string(buf[:4]) + "-" + string(buf[4:])
}
