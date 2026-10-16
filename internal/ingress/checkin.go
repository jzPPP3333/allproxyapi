package ingress

import (
	"context"
	"log"
	"time"

	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

// A provider's check-in is an optional daily action that keeps an account's
// free allowance topped up. Adapters that have no check-in concept implement it
// as CheckinUnsupported by returning the zero result.

// RunCheckin performs the daily check-in for one provider across every account
// of that provider. A provider without check-in support reports every account
// as unsupported rather than failing.
func (s *Server) RunCheckin(ctx context.Context, provider string, accountID string) (summary pool.CheckinSummary) {
	summary.StartedAt = time.Now()
	// The named return is what the deferred assignment must reach; assigning to
	// a plain local here would be discarded when the value is copied out.
	defer func() { summary.FinishedAt = time.Now() }()

	up, ok := s.upstreams.GetByName(provider)
	if !ok {
		summary.Tally(pool.CheckinResult{
			Provider: provider,
			Status:   pool.CheckinFailed,
			Message:  "unknown provider",
			At:       time.Now(),
		})
		return summary
	}

	accounts := s.pool.ListByType(up.ID())
	for _, acc := range accounts {
		if accountID != "" && acc.ID != accountID {
			continue
		}

		// Check-in must not resurrect a deliberately parked account; an
		// exhausted one will simply fail the call again.
		result, err := up.Checkin(ctx, acc)
		if err != nil {
			result = pool.CheckinResult{
				Provider:    provider,
				AccountID:   acc.ID,
				AccountName: acc.Name,
				Status:      pool.CheckinFailed,
				Message:     err.Error(),
			}
		}
		if result.Provider == "" {
			result.Provider = provider
		}
		if result.AccountID == "" {
			result.AccountID = acc.ID
		}
		if result.AccountName == "" {
			result.AccountName = acc.Name
		}
		if result.At.IsZero() {
			result.At = time.Now()
		}
		summary.Tally(result)

		log.Printf("[Checkin] %s 账号 %s: %s %s", up.Label(), acc.Name, result.Status, result.Message)
	}

	return summary
}

// RunCheckinAll performs the daily check-in for every provider.
func (s *Server) RunCheckinAll(ctx context.Context) (combined pool.CheckinSummary) {
	combined.StartedAt = time.Now()
	defer func() { combined.FinishedAt = time.Now() }()

	for _, up := range s.upstreams.All() {
		part := s.RunCheckin(ctx, string(up.ID()), "")
		combined.Attempted += part.Attempted
		combined.Succeeded += part.Succeeded
		combined.AlreadyDone += part.AlreadyDone
		combined.Unsupported += part.Unsupported
		combined.Failed += part.Failed
		combined.Results = append(combined.Results, part.Results...)
	}

	return combined
}
