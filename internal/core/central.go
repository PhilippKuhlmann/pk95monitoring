package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// centralClient sends the summary; its timeout bounds every push.
var centralClient = &http.Client{Timeout: 20 * time.Second}

// pushCentral posts the summary to the central instance when enabled. The first failure
// becomes a warning event, the first success after failures an info event.
func (c *Core) pushCentral(ctx context.Context) {
	cs := c.Settings().Central
	if !cs.Enabled || cs.URL == "" {
		return
	}
	err := c.postSummary(ctx, cs)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		if c.centralFail.CompareAndSwap(false, true) {
			c.Event("warning", "Zentrale", "Übertragung an die Zentrale fehlgeschlagen", err.Error(), nil, false)
		}
		return
	}
	if c.centralFail.CompareAndSwap(true, false) {
		c.Event("info", "Zentrale", "Übertragung an die Zentrale funktioniert wieder", cs.URL, nil, false)
	}
}

func (c *Core) postSummary(ctx context.Context, cs CentralSettings) error {
	body, err := json.Marshal(c.Summary())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cs.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("Ungültige Adresse: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cs.Token)
	req.Header.Set("User-Agent", "PK95-Monitoring/"+c.Opt.Version)
	res, err := centralClient.Do(req)
	if err != nil {
		return fmt.Errorf("Zentrale nicht erreichbar: %w", err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("Zentrale antwortet mit HTTP %d", res.StatusCode)
	}
	return nil
}
