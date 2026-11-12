package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kardianos/service"

	"pk95monitoring/internal/agent"
)

// systemdUnit replaces kardianos' default unit (RestartSec=120) so a crashed or updated agent
// is back within seconds. Syntax: kardianos' mini template engine.
const systemdUnit = `[Unit]
Description={{Description}}
ConditionFileIsExecutable={{Path | cmdEscape}}
After=network-online.target
Wants=network-online.target

[Service]
ExecStart={{Path | cmdEscape}}{{range Arguments}} {{. | cmd}}{{end}}
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
`

type program struct {
	cfgPath string
	log     *slog.Logger
	cancel  context.CancelFunc
	done    chan struct{}
}

func (p *program) Start(service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel, p.done = cancel, make(chan struct{})
	go func() {
		defer close(p.done)
		p.loop(ctx)
	}()
	return nil
}

func (p *program) Stop(service.Service) error {
	p.cancel()
	select {
	case <-p.done:
	case <-time.After(20 * time.Second):
		p.log.Warn("agent did not stop in time")
	}
	return nil
}

// loop runs the agent; a missing or broken config is retried every minute instead of letting
// the service manager restart the process in a tight loop.
func (p *program) loop(ctx context.Context) {
	for {
		a, err := agent.New(p.cfgPath, p.log)
		if err == nil {
			err = a.Run(ctx)
			if errors.Is(err, agent.ErrRestart) {
				exe := ""
				if a.Updater != nil {
					exe = a.Updater.Exe
				}
				p.log.Info("starting new version")
				if rerr := agent.RestartSelf(exe); rerr != nil {
					p.log.Error("restart failed", "err", rerr)
					os.Exit(1) // the service manager starts the new binary
				}
			}
			if err == nil || ctx.Err() != nil {
				return
			}
		}
		p.log.Error("agent cannot start, retrying in 1 minute", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

func newService(cfgPath, exe string) (service.Service, error) {
	abs, err := filepath.Abs(cfgPath)
	if err != nil {
		return nil, err
	}
	return newServiceWith(&program{cfgPath: abs, log: slog.Default()}, abs, exe)
}

func newServiceWith(p *program, cfgPath, exe string) (service.Service, error) {
	return service.New(p, &service.Config{
		Name:        agent.ServiceName,
		DisplayName: agent.ServiceDisplayName,
		Description: agent.ServiceDescription,
		Executable:  exe,
		Arguments:   []string{"run", "--config", cfgPath},
		Option: service.KeyValue{
			"Restart":       "always",
			"SystemdScript": systemdUnit,
			// Windows: after a self-update the agent exits with an error code and the
			// recovery action starts the new binary.
			"OnFailure":              "restart",
			"OnFailureDelayDuration": "5s",
			"OnFailureResetPeriod":   86400,
			"DelayedAutoStart":       true,
		},
	})
}

// runService runs the agent under the service manager, or in the foreground until Ctrl+C.
func runService(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := configFlag(fs)
	_ = fs.Parse(args)
	abs, err := filepath.Abs(*cfgPath)
	if err != nil {
		return err
	}
	if _, err := agent.Load(abs); err != nil && service.Interactive() {
		return fmt.Errorf("%w – zuerst „pk95agent install“ ausführen", err)
	}
	exe, _ := os.Executable()
	log := slog.New(slog.NewTextHandler(logWriter(abs), nil))
	slog.SetDefault(log)
	s, err := newServiceWith(&program{cfgPath: abs, log: log}, abs, exe)
	if err != nil {
		return err
	}
	return s.Run()
}

// logWriter: as a Windows service stderr goes nowhere, so the log goes to agent.log next to
// the config (rotated at 5 MB). systemd collects stderr in the journal.
func logWriter(cfgPath string) io.Writer {
	if runtime.GOOS != "windows" || service.Interactive() {
		return os.Stderr
	}
	path := filepath.Join(filepath.Dir(cfgPath), "agent.log")
	if st, err := os.Stat(path); err == nil && st.Size() > 5<<20 {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return os.Stderr
	}
	return f
}

// waitStopped waits up to 20 s until the service has stopped (its binary is then free).
func waitStopped(s service.Service) {
	for range 40 {
		if st, err := s.Status(); err != nil || st != service.StatusRunning {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// installBinary copies the running executable to target unless it already runs from there.
func installBinary(target string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	if samePath(self, target) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	src, err := os.Open(self)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp := target + ".new"
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, target)
		if err != nil && runtime.GOOS == "windows" {
			// Target still locked by a running process: move it aside (allowed while running).
			_ = os.Remove(target + ".old")
			if os.Rename(target, target+".old") == nil {
				err = os.Rename(tmp, target)
			}
		}
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	fmt.Printf("Programm installiert: %s\n", target)
	return nil
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// removeConfig deletes config, buffer and logs and the directory if it is empty then.
func removeConfig(cfgPath string) bool {
	dir := filepath.Dir(cfgPath)
	removed := false
	for _, p := range []string{cfgPath, agent.BufferPath(cfgPath), filepath.Join(dir, "agent.log"), filepath.Join(dir, "agent.log.1")} {
		if os.Remove(p) == nil {
			removed = true
		}
	}
	_ = os.Remove(dir) // only succeeds when empty
	return removed
}
