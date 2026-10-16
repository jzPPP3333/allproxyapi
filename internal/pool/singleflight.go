package pool

import "sync"

// A token that is about to expire tends to be noticed by several concurrent
// requests at once. Without coordination each one calls the provider to refresh
// the same account, which wastes quota at best and can trip a rate limit or
// invalidate the credential at worst, since some providers rotate the refresh
// token on use and only the last response is valid.

// refreshGroup deduplicates concurrent refreshes, keyed by account.
//
// The first caller for an account performs the work; the others wait for it and
// receive its result. This is a small purpose-built equivalent of
// golang.org/x/sync/singleflight, kept local so the gateway stays free of that
// dependency.
type refreshGroup struct {
	mu    sync.Mutex
	calls map[string]*refreshCall
}

type refreshCall struct {
	done chan struct{}
	err  error
}

func newRefreshGroup() *refreshGroup {
	return &refreshGroup{calls: make(map[string]*refreshCall)}
}

// Do runs fn for the key, or joins an in-flight run of the same key.
//
// fn is called at most once per concurrent batch. Every caller, including the
// one that ran fn, receives the same error.
func (g *refreshGroup) Do(key string, fn func() error) error {
	g.mu.Lock()
	if call, ok := g.calls[key]; ok {
		g.mu.Unlock()
		<-call.done
		return call.err
	}
	call := &refreshCall{done: make(chan struct{})}
	g.calls[key] = call
	g.mu.Unlock()

	// fn runs outside the lock so other keys are not blocked behind it.
	func() {
		defer func() {
			g.mu.Lock()
			delete(g.calls, key)
			g.mu.Unlock()
			close(call.done)
		}()

		if fn == nil {
			return
		}
		call.err = fn()
	}()

	return call.err
}

// For single-flight refresh coordination, one group per gateway is enough since
// account IDs are unique across providers.
var globalRefreshGroup = newRefreshGroup()

// singleflightRefresh wraps a refresh handler so concurrent refreshes of the
// same account collapse into one provider call.
func singleflightRefresh(acc *Account, fn func() error) error {
	if acc == nil {
		return fn()
	}
	return globalRefreshGroup.Do(acc.ID, fn)
}
