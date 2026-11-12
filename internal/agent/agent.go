package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"pk95monitoring/internal/model"
)

// unauthorizedRetry is the report interval while the server rejects the token.
const unauthorizedRetry = 10 * time.Minute

// flushPerCycle limits the buffered reports sent after one live report (5 h at 60 s).
const flushPerCycle = 300

// Reporter produces a report of this host (*Collector; replaced in tests).
type Reporter interface {
	Report(ctx context.Context, cfg Config) model.AgentReport
}

// Agent is the service: collect, report, buffer, apply the server's configuration, update.
type Agent struct {
	ConfigPath string
	Config     Config
	Client     *Client
	Buffer     *Buffer // optional: without it, undelivered reports are lost
	Collector  Reporter
	Updater    *Updater // optional
	Log        *slog.Logger

	failures int // consecutive failed reports (to log the first one and the recovery)
}

// New loads the config and wires client, buffer, collector and updater.
func New(configPath string, log *slog.Logger) (*Agent, error) {
	if log == nil {
		log = slog.Default()
	}
	cfg, err := Load(configPath)
	if err != nil {
		return nil, err
	}
	client, err := NewClient(cfg.Server, cfg.Token, cfg.Fingerprint, cfg.Insecure)
	if err != nil {
		return nil, err
	}
	a := &Agent{ConfigPath: configPath, Config: cfg, Client: client, Collector: NewCollector(), Log: log}
	if a.Buffer, err = OpenBuffer(BufferPath(configPath), MaxBuffered); err != nil {
		log.Error("buffer unavailable, reports are lost while the server is unreachable", "err", err)
		a.Buffer = nil
	}
	if a.Updater, err = NewUpdater(client, log); err != nil {
		log.Warn("self-update unavailable", "err", err)
		a.Updater = nil
	}
	return a, nil
}

// Run reports every interval until ctx ends. It returns ErrRestart after a self-update.
func (a *Agent) Run(ctx context.Context) error {
	a.Log.Info("agent started", "version", Version, "server", a.Config.Server,
		"interval", a.Config.IntervalSeconds(), "buffered", a.bufferLen())
	for {
		start := time.Now()
		wait, err := a.Cycle(ctx)
		if err != nil {
			return err
		}
		t := time.NewTimer(max(wait-time.Since(start), time.Second))
		select {
		case <-ctx.Done():
			t.Stop()
			a.Log.Info("agent stopped")
			return nil
		case <-t.C:
		}
	}
}

// Cycle collects and sends one report, flushes the buffer after a successful send and applies
// the reply. It returns the time until the next cycle; the only error is ErrRestart.
func (a *Agent) Cycle(ctx context.Context) (time.Duration, error) {
	rep := a.Collector.Report(ctx, a.Config)
	if ctx.Err() != nil {
		return 0, nil
	}
	reply, err := a.Client.Report(ctx, rep)
	if err != nil {
		return a.failed(ctx, rep, err), nil
	}
	if a.failures > 0 {
		a.Log.Info("server reachable again", "failures", a.failures)
		a.failures = 0
	}
	a.applyReply(reply)
	a.flush(ctx)
	if reply.Update != nil && a.Updater != nil {
		if err := a.Updater.Apply(ctx, *reply.Update); errors.Is(err, ErrRestart) {
			return 0, err
		}
	}
	return a.interval(), nil
}

func (a *Agent) interval() time.Duration {
	return time.Duration(a.Config.IntervalSeconds()) * time.Second
}

// failed buffers an undelivered report (unless the server rejected it for good) and returns
// the wait until the next attempt.
func (a *Agent) failed(ctx context.Context, rep model.AgentReport, err error) time.Duration {
	if ctx.Err() != nil {
		// Stopped while sending: keep the report for the next start.
		a.buffer(rep)
		return 0
	}
	a.failures++
	wait := a.interval()
	switch {
	case IsPermanent(err):
		a.Log.Error("server rejected the report, discarding it", "err", err)
		return wait
	case errors.Is(err, ErrUnauthorized):
		a.Log.Error("token invalid, register the agent again (pk95agent install ...)", "err", err)
		wait = max(wait, unauthorizedRetry)
	case a.failures == 1 || a.failures%60 == 0:
		a.Log.Warn("server unreachable, buffering reports", "err", err, "failures", a.failures)
	}
	a.buffer(rep)
	return wait
}

func (a *Agent) buffer(rep model.AgentReport) {
	if a.Buffer == nil {
		return
	}
	if err := a.Buffer.Add(rep); err != nil {
		a.Log.Error("cannot buffer report", "err", err)
	}
}

// flush sends buffered reports, oldest first.
func (a *Agent) flush(ctx context.Context) {
	if a.Buffer == nil || a.Buffer.Len() == 0 {
		return
	}
	n, err := a.Buffer.Flush(flushPerCycle, func(r model.AgentReport) error {
		_, err := a.Client.Report(ctx, r)
		if IsPermanent(err) {
			a.Log.Warn("server rejected buffered report, discarded", "err", err, "time", r.Time)
		}
		return err
	})
	if n > 0 {
		a.Log.Info("sent buffered reports", "count", n, "remaining", a.Buffer.Len())
	}
	if err != nil && ctx.Err() == nil {
		a.Log.Warn("sending buffered reports interrupted", "err", err, "remaining", a.Buffer.Len())
	}
}

// applyReply takes over the configuration the server sent and persists changes.
func (a *Agent) applyReply(r model.AgentReply) {
	if !a.Config.applyReply(r.Interval, r.Services, r.WatchAutoServices, r.IgnoreServices) {
		return
	}
	a.Log.Info("applied configuration from server", "interval", a.Config.IntervalSeconds(),
		"services", len(a.Config.Services), "auto_services", a.Config.WatchAutoServices)
	if a.ConfigPath == "" {
		return
	}
	if err := Save(a.ConfigPath, a.Config); err != nil {
		a.Log.Error("cannot save configuration", "err", err)
	}
}

func (a *Agent) bufferLen() int {
	if a.Buffer == nil {
		return 0
	}
	return a.Buffer.Len()
}
