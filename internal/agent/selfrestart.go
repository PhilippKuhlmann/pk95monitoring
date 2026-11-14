package agent

import (
	"slices"
	"strings"

	"pk95monitoring/internal/eventrules"
	"pk95monitoring/internal/model"
)

// selfRestartWindow: an event this long before or after a recorded self-update restart is
// caused by it (the service manager logs it within seconds).
const selfRestartWindow = 120

// selfRestartEvent reports whether e is the service manager's "terminated unexpectedly"
// (7031, 7034) of this agent caused by a restart for a self-update. Events without a message
// (only the newest ten carry one) count by their time alone.
func selfRestartEvent(e model.EventEntry, restarts []int64) bool {
	if e.Source != "Service Control Manager" || e.ID != 7031 && e.ID != 7034 {
		return false
	}
	if e.Message != "" && !strings.Contains(e.Message, ServiceDisplayName) {
		return false
	}
	return slices.ContainsFunc(restarts, func(t int64) bool { return e.Time >= t-5 && e.Time <= t+selfRestartWindow })
}

// dropSelfRestarts removes the events of the agent's own self-update restarts.
func dropSelfRestarts(res map[string]eventrules.Result, restarts []int64) map[string]eventrules.Result {
	if len(restarts) == 0 {
		return res
	}
	out := make(map[string]eventrules.Result, len(res))
	for id, r := range res {
		r.Events = slices.DeleteFunc(slices.Clone(r.Events), func(e model.EventEntry) bool { return selfRestartEvent(e, restarts) })
		out[id] = r
	}
	return out
}
