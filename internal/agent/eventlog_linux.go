package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"pk95monitoring/internal/eventrules"
)

// queryEvents runs one journalctl per rule: the rule's arguments, --since the start of its
// window and, for text filters, --grep (case-insensitive). journalctl without pattern
// matching support is asked again without --grep; the text is then checked by the rules.
func (p *platformState) queryEvents(ctx context.Context) (map[string]eventrules.Result, error) {
	p.evMu.Lock()
	qs := p.evQueries
	p.evMu.Unlock()
	res := make(map[string]eventrules.Result, len(qs))
	now := time.Now()
	for _, q := range qs {
		args := append(append([]string{}, q.Args...), "--since=@"+strconv.FormatInt(now.Add(-time.Duration(q.Hours)*time.Hour).Unix(), 10))
		grep := q.Grep != "" && !p.noGrep.Load()
		if grep {
			args = append(args, "--grep="+q.Grep, "--case-sensitive=false")
		}
		out, stderr, err := journal(ctx, args)
		if err != nil && grep && strings.Contains(strings.ToLower(stderr), "pattern") {
			p.noGrep.Store(true)
			out, stderr, err = journal(ctx, args[:len(args)-2])
		}
		r := eventrules.Result{Events: parseJournal(out)}
		var ee *exec.ExitError
		switch {
		case err == nil:
		case errors.As(err, &ee) && len(r.Events) == 0 && (strings.TrimSpace(stderr) == "" || strings.Contains(stderr, "No entries")):
			// --grep without a match exits with 1
		default:
			r.Err = "journalctl: " + truncate(strings.TrimSpace(err.Error()+" "+stderr), 200)
		}
		r.Full = len(r.Events) >= q.Max
		res[q.ID] = r
		if ctx.Err() != nil {
			return nil, fmt.Errorf("Journal: %w", ctx.Err())
		}
	}
	return res, nil
}

// journal runs journalctl and returns its standard output and error output.
func journal(ctx context.Context, args []string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "SYSTEMD_PAGER=")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}
