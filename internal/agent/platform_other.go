//go:build !linux && !windows

package agent

import (
	"context"

	"pk95monitoring/internal/model"
)

// platformState is empty: other systems (macOS, BSD) only get the generic checks.
type platformState struct{}

func platformMachineID() string { return "" }

func (c *Collector) platformChecks(context.Context, Config, HostInfo) []model.Check { return nil }
