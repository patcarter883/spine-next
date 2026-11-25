package reconciler

import (
	"context"
	"fmt"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
)

// A run lost to an unavailable provider (api.Outcome.Provider unreachable
// or rate_limit) is not charged to the task: the same round runs again
// after ProviderBackoff, doubling up to ProviderBackoffMax, and the task
// escalates as ProviderUnavailable once the outage has lasted
// ProviderOutageLimit.
var (
	ProviderBackoff     = 15 * time.Second
	ProviderBackoffMax  = 4 * time.Minute
	ProviderOutageLimit = 30 * time.Minute
)

// outage is an adapter whose provider is unavailable. No run starts on it
// before until; then one task's run (the probe) finds out whether it is back.
type outage struct {
	since time.Time
	until time.Time
	probe api.TaskID
	class api.ProviderFailure
}

func providerBackoff(failures int) time.Duration {
	d := ProviderBackoff
	for i := 1; i < failures && d < ProviderBackoffMax; i++ {
		d *= 2
	}
	return min(d, ProviderBackoffMax)
}

func (c *Controller) adapterName(t *api.Task) string {
	if t.Spec.Adapter != "" {
		return t.Spec.Adapter
	}
	return c.Config.Agent
}

// providerHeld reports why task id may not start a run on adapter name.
// Once the adapter's backoff has expired one run may start, and claim
// makes task id that run (the probe).
func (c *Controller) providerHeld(name string, id api.TaskID, claim bool) (string, time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	o := c.outages[name]
	if o == nil || o.probe == id {
		return "", 0, false
	}
	if _, probing := c.inflight[o.probe]; o.probe != "" && probing || time.Now().Before(o.until) {
		msg := fmt.Sprintf("waiting for adapter %s: provider %s since %s", name, describeProvider(o.class), o.since.Format(time.RFC3339))
		return msg, max(time.Until(o.until), time.Second), true
	}
	if claim {
		o.probe = id
	}
	return "", 0, false
}

// providerOutcome records what a run on adapter name says about its
// provider, in the status update that records the outcome.
func providerOutcome(s *api.Status, out *api.Outcome, since time.Time) {
	if !retryableProvider(out.Provider) {
		s.ProviderFailures, s.ProviderDownSince, s.ProviderRetryAt = 0, time.Time{}, time.Time{}
		return
	}
	now := time.Now().UTC()
	if s.ProviderFailures == 0 {
		s.ProviderDownSince = now
		if !since.IsZero() && since.Before(now) {
			s.ProviderDownSince = since
		}
	}
	s.ProviderFailures++
	s.ProviderRetryAt = now.Add(providerBackoff(s.ProviderFailures))
}

// noteProvider updates the adapter's outage after a run's outcome.
func (c *Controller) noteProvider(name string, id api.TaskID, out api.Outcome, retryAt time.Time) {
	c.mu.Lock()
	o := c.outages[name]
	if !retryableProvider(out.Provider) {
		if o != nil && (o.probe == id || o.probe == "") {
			delete(c.outages, name)
			c.mu.Unlock()
			c.event(id, "provider_back", map[string]any{"adapter": name, "down_since": o.since})
			return
		}
		c.mu.Unlock()
		return
	}
	if o == nil {
		o = &outage{since: time.Now().UTC()}
		c.outages[name] = o
	}
	o.class = out.Provider
	if retryAt.After(o.until) {
		o.until = retryAt
	}
	if o.probe == id {
		o.probe = ""
	}
	c.mu.Unlock()
}

func (c *Controller) outageSince(name string) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if o := c.outages[name]; o != nil {
		return o.since
	}
	return time.Time{}
}

// restoreOutage rebuilds an adapter's outage from a task waiting on it
// (after a controller restart).
func (c *Controller) restoreOutage(t *api.Task) {
	out := t.Status.LastOutcome
	if out == nil || !retryableProvider(out.Provider) || t.Status.ProviderFailures == 0 {
		return
	}
	name := c.adapterName(t)
	o := c.outages[name]
	if o == nil {
		o = &outage{since: t.Status.ProviderDownSince, class: out.Provider}
		c.outages[name] = o
	}
	if t.Status.ProviderDownSince.Before(o.since) {
		o.since = t.Status.ProviderDownSince
	}
	if t.Status.ProviderRetryAt.After(o.until) {
		o.until = t.Status.ProviderRetryAt
	}
}

func retryableProvider(p api.ProviderFailure) bool {
	return p == api.ProviderUnreachable || p == api.ProviderRateLimit
}

func describeProvider(p api.ProviderFailure) string {
	switch p {
	case api.ProviderRateLimit:
		return "rate-limited"
	case api.ProviderAuth:
		return "rejected the credentials"
	}
	return "unreachable"
}

// providerFailed handles a run that never reached the model: the round is
// run again once the backoff expires, without asking the supervisor or
// charging the budget, until the outage outlasts ProviderOutageLimit.
func (c *Controller) providerFailed(ctx context.Context, t *api.Task, b api.Budget, out *api.Outcome) (Result, error) {
	name := c.adapterName(t)
	if out.Provider == api.ProviderAuth {
		return c.escalate(ctx, t, "ProviderAuthFailed", fmt.Sprintf("adapter %s: the provider rejected the credentials: %s", name, trunc(out.Message, 1000)))
	}
	s := t.Status
	if since := s.ProviderDownSince; !since.IsZero() && time.Since(since) >= ProviderOutageLimit {
		return c.escalate(ctx, t, "ProviderUnavailable", fmt.Sprintf("adapter %s: provider %s for %s (%d runs); last error: %s",
			name, describeProvider(out.Provider), time.Since(since).Round(time.Second), s.ProviderFailures, trunc(out.Message, 1000)))
	}
	if wait := time.Until(s.ProviderRetryAt); wait > 0 {
		msg := fmt.Sprintf("adapter %s: provider %s; run %d retries at %s", name, describeProvider(out.Provider), s.ProviderFailures+1, s.ProviderRetryAt.Format(time.RFC3339))
		if s.Reason != "WaitingForProvider" || s.Message != msg {
			if _, err := c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceController, func(st *api.Status) error {
				st.Reason, st.Message = "WaitingForProvider", msg
				return nil
			}); err != nil {
				return Result{}, err
			}
		}
		return Result{RequeueAfter: wait}, nil
	}
	return c.startAgent(ctx, t, b, s.PatchRound, s.NextFeedback)
}
