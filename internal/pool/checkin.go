package pool

import "time"

// CheckinStatus is the outcome of one daily check-in attempt.
type CheckinStatus string

const (
	// CheckinOK means the check-in was accepted and a reward was granted.
	CheckinOK CheckinStatus = "ok"
	// CheckinAlready means the account had already checked in today.
	CheckinAlready CheckinStatus = "already"
	// CheckinUnsupported means the provider has no check-in concept, or the
	// endpoint is not configured for it.
	CheckinUnsupported CheckinStatus = "unsupported"
	// CheckinFailed means the attempt was made and rejected or errored.
	CheckinFailed CheckinStatus = "failed"
)

// CheckinResult records one account's check-in outcome so the dashboard and the
// scheduler can report what happened without re-deriving it.
type CheckinResult struct {
	Provider    string        `json:"provider"`
	AccountID   string        `json:"account_id"`
	AccountName string        `json:"account_name"`
	Status      CheckinStatus `json:"status"`
	Message     string        `json:"message,omitempty"`
	Rewards     []string      `json:"rewards,omitempty"`
	At          time.Time     `json:"at"`
}

// OK reports whether the check-in succeeded, whether or not a reward was given.
func (r CheckinResult) OK() bool {
	return r.Status == CheckinOK || r.Status == CheckinAlready
}

// CheckinSummary aggregates a batch of check-in results for one run.
type CheckinSummary struct {
	Attempted   int             `json:"attempted"`
	Succeeded   int             `json:"succeeded"`
	AlreadyDone int             `json:"already_done"`
	Unsupported int             `json:"unsupported"`
	Failed      int             `json:"failed"`
	StartedAt   time.Time       `json:"started_at"`
	FinishedAt  time.Time       `json:"finished_at"`
	Results     []CheckinResult `json:"results"`
}

// Tally counts one result into the summary.
func (s *CheckinSummary) Tally(r CheckinResult) {
	s.Attempted++
	switch r.Status {
	case CheckinOK:
		s.Succeeded++
	case CheckinAlready:
		s.AlreadyDone++
	case CheckinUnsupported:
		s.Unsupported++
	default:
		s.Failed++
	}
	s.Results = append(s.Results, r)
}
