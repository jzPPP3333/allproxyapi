package pool

import (
	"math/rand"
	"sort"
	"sync"
	"time"
)

// Dispatch selects an account for a request.
//
// The order is: drop accounts that cannot serve the model, prefer the ones
// whose free allowance is closest to expiring, then choose among the best few
// at random rather than always taking the single best. Always taking the first
// candidate is what turns a burst of concurrent requests into an immediate
// rate limit on one account.

// DispatchCandidateCount is how many of the best accounts form the random
// shortlist. Choosing from a small pool spreads a burst while still preferring
// the highest-value accounts.
const DispatchCandidateCount = 5

// DispatchOptions describes what a request needs.
type DispatchOptions struct {
	// Model is the upstream model the request will use, used both for
	// capability matching and for per-model cooldown checks.
	Model string
	// Provider restricts selection to one provider.
	Provider AccountType
	// AffinityKey identifies a conversation. When set, a recently used account
	// for this key is preferred so multi-turn requests stay on one account
	// rather than spreading a conversation across accounts.
	AffinityKey string
}

// affinityTTL is how long a session-to-account binding is honoured.
const affinityTTL = 30 * time.Minute

// affinityLimit bounds the binding table so a long-lived process cannot grow it
// without limit.
const affinityLimit = 4096

// affinityEntry binds a conversation key to an account.
type affinityEntry struct {
	accountID string
	at        time.Time
}

// affinityTracker remembers which account served a conversation.
type affinityTracker struct {
	mu      sync.Mutex
	entries map[string]affinityEntry
	// fallback is the source of randomness; injectable for deterministic tests.
	rand *rand.Rand
}

func newAffinityTracker(seed int64) *affinityTracker {
	return &affinityTracker{
		entries: make(map[string]affinityEntry),
		rand:    rand.New(rand.NewSource(seed)),
	}
}

// lookup returns the bound account for a key, if the binding is still fresh.
func (t *affinityTracker) lookup(key string) (string, bool) {
	if key == "" {
		return "", false
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	e, ok := t.entries[key]
	if !ok || time.Since(e.at) > affinityTTL {
		if ok {
			delete(t.entries, key)
		}
		return "", false
	}
	return e.accountID, true
}

// bind records that an account served a conversation.
func (t *affinityTracker) bind(key, accountID string) {
	if key == "" || accountID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.entries) >= affinityLimit {
		t.pruneLocked()
	}
	t.entries[key] = affinityEntry{accountID: accountID, at: time.Now()}
}

// forget drops a binding, used when the bound account becomes unusable.
func (t *affinityTracker) forget(key string) {
	if key == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, key)
}

// pruneLocked drops expired bindings, and if that is not enough, the oldest
// half so the table stays bounded under many distinct conversations.
func (t *affinityTracker) pruneLocked() {
	now := time.Now()
	for k, e := range t.entries {
		if now.Sub(e.at) > affinityTTL {
			delete(t.entries, k)
		}
	}
	if len(t.entries) < affinityLimit {
		return
	}

	type kv struct {
		k  string
		at time.Time
	}
	all := make([]kv, 0, len(t.entries))
	for k, e := range t.entries {
		all = append(all, kv{k, e.at})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })
	for i := 0; i < len(all)/2; i++ {
		delete(t.entries, all[i].k)
	}
}

// intn returns a random int in [0,n) using the tracker's source.
func (t *affinityTracker) intn(n int) int {
	if n <= 1 {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.rand.Intn(n)
}

// Pick selects an account for a request.
//
// Selection order: health filter, capability match, then prefer accounts whose
// credit expires soonest, choosing at random among the best few. When nothing
// is available it returns nil, and the caller decides whether that is a hard
// failure or a retryable one.
func (p *AccountPool) PickFor(opts DispatchOptions) *Account {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()

	// A conversation already bound to a healthy account stays there.
	if opts.AffinityKey != "" {
		if id, ok := p.affinity.lookup(opts.AffinityKey); ok {
			if acc := p.accounts[id]; acc != nil &&
				acc.Type == opts.Provider &&
				acc.AvailableFor(opts.Model) &&
				accountSupportsModel(acc, opts.Model) {
				return acc
			}
			// The binding is no longer usable; drop it so a fresh choice wins.
			p.affinity.forget(opts.AffinityKey)
		}
	}

	eligible := p.eligibleLocked(opts)

	// Nothing healthy: fall back to whichever account recovers soonest, so a
	// single-account deployment still makes progress instead of failing
	// outright.
	if len(eligible) == 0 {
		return p.firstRecoveringLocked(opts)
	}

	selected := p.selectFromLocked(eligible, now)

	// Bind the conversation so later turns reuse the same account.
	if opts.AffinityKey != "" && selected != nil {
		p.affinity.bind(opts.AffinityKey, selected.ID)
	}
	return selected
}

// eligibleLocked returns the accounts that can serve the request right now.
func (p *AccountPool) eligibleLocked(opts DispatchOptions) []*Account {
	var list []*Account
	for _, acc := range p.accounts {
		if opts.Provider != "" && acc.Type != opts.Provider {
			continue
		}
		if !acc.AvailableFor(opts.Model) {
			continue
		}
		if !accountSupportsModel(acc, opts.Model) {
			continue
		}
		list = append(list, acc)
	}
	return list
}

// accountSupportsModel reports whether an account can serve a model.
//
// An account with no discovered model list is treated as capable: refusing it
// would take a working account out of rotation merely because discovery has not
// run yet. The check only excludes an account that published a list and does
// not include the model.
func accountSupportsModel(acc *Account, model string) bool {
	if model == "" {
		return true
	}
	models := acc.GetModels()
	if len(models) == 0 {
		return true
	}
	for _, m := range models {
		if m.ID == model {
			return true
		}
	}
	return false
}

// selectFromLocked ranks candidates by value and picks among the best few.
//
// Selection within the shortlist is weighted by rank rather than uniform: the
// best candidate is most likely, each subsequent one less so. Uniform choice
// would give the least valuable candidate the same chance as the best, which in
// a small pool means the value ordering stops mattering. Weighting keeps the
// spread that avoids piling a burst onto one account while still preferring the
// accounts whose allowance is worth spending first.
func (p *AccountPool) selectFromLocked(eligible []*Account, now time.Time) *Account {
	sort.SliceStable(eligible, func(i, j int) bool {
		aKey, aLoad := dispatchRank(eligible[i], now)
		bKey, bLoad := dispatchRank(eligible[j], now)
		if aKey != bKey {
			return aKey < bKey
		}
		return aLoad < bLoad
	})

	n := DispatchCandidateCount
	if n > len(eligible) {
		n = len(eligible)
	}
	shortlist := eligible[:n]

	// Weights n, n-1, ... 1 over the shortlist; every candidate stays
	// reachable, so a lower-ranked account is still used occasionally.
	total := n * (n + 1) / 2
	r := p.affinity.intn(total)
	for i := 0; i < n; i++ {
		weight := n - i
		if r < weight {
			return shortlist[i]
		}
		r -= weight
	}
	return shortlist[n-1]
}

// dispatchRank orders accounts so the most valuable comes first. Lower is
// better.
//
// Accounts whose credit expires soonest come first, because unused daily
// allowance is lost rather than carried over. Ties break on least outstanding
// work, which keeps a burst from piling onto one account.
func dispatchRank(acc *Account, now time.Time) (int64, int) {
	acc.mu.RLock()
	credit := acc.CreditExpiresAt
	inflight := acc.InFlight
	acc.mu.RUnlock()

	// An account with no known expiry sorts last: it is not at risk of losing
	// allowance, so it is the safest one to leave for later.
	var creditKey int64 = 1<<62 - 1
	if !credit.IsZero() {
		creditKey = credit.Unix()
	}
	return creditKey, inflight
}

// firstRecoveringLocked returns the account whose cooldown lifts soonest, used
// when nothing is currently healthy.
func (p *AccountPool) firstRecoveringLocked(opts DispatchOptions) *Account {
	var best *Account
	var bestUntil time.Time

	for _, acc := range p.accounts {
		if opts.Provider != "" && acc.Type != opts.Provider {
			continue
		}
		// Disabled accounts never recover on their own.
		if acc.State() == StateDisabled {
			continue
		}
		if !accountSupportsModel(acc, opts.Model) {
			continue
		}

		until := acc.recoveryTime()
		if best == nil || until.Before(bestUntil) {
			best, bestUntil = acc, until
		}
	}
	return best
}

// recoveryTime is when the account is expected to become usable again.
func (a *Account) recoveryTime() time.Time {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.hardCooldownUntil.After(a.CooldownUntil) {
		return a.hardCooldownUntil
	}
	return a.CooldownUntil
}

// RecordDispatch notes that a request was handed to an account, so concurrent
// work is visible to the ranker.
func (a *Account) RecordDispatch() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.InFlight++
}

// ReleaseDispatch notes that a request to the account finished.
func (a *Account) ReleaseDispatch() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.InFlight > 0 {
		a.InFlight--
	}
}
