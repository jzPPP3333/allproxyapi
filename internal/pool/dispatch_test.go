package pool

import (
	"testing"
	"time"
)

// withModels gives an account a discovered model list.
func withModels(acc *Account, ids ...string) *Account {
	ms := make([]ModelMetadata, 0, len(ids))
	for _, id := range ids {
		ms = append(ms, ModelMetadata{ID: id, Name: id})
	}
	acc.SetModels(ms)
	return acc
}

func TestPickForSkipsDisabledAndCooling(t *testing.T) {
	p := newAccountPoolWithSeed(1)

	disabled := &Account{ID: "disabled", Type: TypeQoder}
	disabled.Disable("bad")
	cooling := &Account{ID: "cooling", Type: TypeQoder}
	cooling.SetSoftCooldown(time.Hour, "429")
	healthy := &Account{ID: "healthy", Type: TypeQoder}

	p.AddOrUpdate(disabled)
	p.AddOrUpdate(cooling)
	p.AddOrUpdate(healthy)

	got := p.PickFor(DispatchOptions{Provider: TypeQoder})
	if got == nil {
		t.Fatal("no account selected")
	}
	if got.ID != "healthy" {
		t.Fatalf("selected %q, want the healthy account", got.ID)
	}
}

// TestPickForMatchesModelCapability is the capability check: an account that
// published a model list without the requested model must not be chosen.
func TestPickForMatchesModelCapability(t *testing.T) {
	p := newAccountPoolWithSeed(1)

	noModel := withModels(&Account{ID: "no-model", Type: TypeQoder}, "other-model")
	hasModel := withModels(&Account{ID: "has-model", Type: TypeQoder}, "wanted-model")

	p.AddOrUpdate(noModel)
	p.AddOrUpdate(hasModel)

	got := p.PickFor(DispatchOptions{Provider: TypeQoder, Model: "wanted-model"})
	if got == nil || got.ID != "has-model" {
		t.Fatalf("selected %v, want the account with the model", got)
	}
}

// TestPickForAllowsUnknownCapability checks an account with no discovered list
// is still eligible: refusing it would drop a working account just because
// discovery has not run.
func TestPickForAllowsUnknownCapability(t *testing.T) {
	p := newAccountPoolWithSeed(1)
	p.AddOrUpdate(&Account{ID: "unknown", Type: TypeQoder})

	got := p.PickFor(DispatchOptions{Provider: TypeQoder, Model: "any-model"})
	if got == nil || got.ID != "unknown" {
		t.Fatalf("an account with no model list was excluded: %v", got)
	}
}

// TestPickForRespectsModelCooldown checks a model-scoped limit excludes the
// account only for that model.
func TestPickForRespectsModelCooldown(t *testing.T) {
	p := newAccountPoolWithSeed(1)

	a := withModels(&Account{ID: "a", Type: TypeQoder}, "limited", "free")
	a.SetModelCooldown("limited", time.Hour, "429")
	b := withModels(&Account{ID: "b", Type: TypeQoder}, "limited", "free")
	p.AddOrUpdate(a)
	p.AddOrUpdate(b)

	// The limited model goes to b, since a is parked for it.
	got := p.PickFor(DispatchOptions{Provider: TypeQoder, Model: "limited"})
	if got == nil || got.ID != "b" {
		t.Fatalf("selected %v for the limited model, want b", got)
	}

	// The free model can still be served by either.
	seen := map[string]bool{}
	for i := 0; i < 40; i++ {
		if acc := p.PickFor(DispatchOptions{Provider: TypeQoder, Model: "free"}); acc != nil {
			seen[acc.ID] = true
		}
	}
	if !seen["a"] {
		t.Error("account a was excluded from a model it is not limited on")
	}
}

// TestDispatchPrefersExpiringCredit checks the value-first ordering: allowance
// that is about to lapse is spent before allowance that is not.
//
// Selection within the shortlist is weighted rather than exclusive, so the
// assertion is on the ordering of usage, not on the best account being the only
// one ever used — keeping lower-ranked candidates reachable is what spreads a
// burst instead of piling it onto one account.
func TestDispatchPrefersExpiringCredit(t *testing.T) {
	p := newAccountPoolWithSeed(2)

	soon := &Account{ID: "soon", Type: TypeQoder, CreditExpiresAt: time.Now().Add(time.Hour)}
	later := &Account{ID: "later", Type: TypeQoder, CreditExpiresAt: time.Now().Add(72 * time.Hour)}
	never := &Account{ID: "never", Type: TypeQoder} // no known expiry
	p.AddOrUpdate(soon)
	p.AddOrUpdate(later)
	p.AddOrUpdate(never)

	counts := map[string]int{}
	for i := 0; i < 600; i++ {
		acc := p.PickFor(DispatchOptions{Provider: TypeQoder})
		if acc == nil {
			t.Fatal("no account selected")
		}
		counts[acc.ID]++
	}
	t.Logf("usage by credit expiry: %v", counts)

	if counts["soon"] <= counts["later"] {
		t.Errorf("credit expiring soonest was not preferred: %v", counts)
	}
	if counts["later"] <= counts["never"] {
		t.Errorf("an account with no expiring credit outranked one with credit: %v", counts)
	}
}

// TestDispatchSpreadsAcrossCandidates checks selection is not deterministic on
// one account, which is what turns a burst into an immediate rate limit.
func TestDispatchSpreadsAcrossCandidates(t *testing.T) {
	p := newAccountPoolWithSeed(3)
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		p.AddOrUpdate(&Account{ID: id, Type: TypeQoder})
	}

	counts := map[string]int{}
	for i := 0; i < 200; i++ {
		acc := p.PickFor(DispatchOptions{Provider: TypeQoder})
		if acc == nil {
			t.Fatal("no account selected")
		}
		counts[acc.ID]++
	}

	if len(counts) < 2 {
		t.Fatalf("all %d picks went to one account: %v", 200, counts)
	}
	t.Logf("distribution over 200 picks: %v", counts)
}

// TestDispatchKeepsSessionAffinity checks consecutive turns of one conversation
// stay on the same account.
func TestDispatchKeepsSessionAffinity(t *testing.T) {
	p := newAccountPoolWithSeed(4)
	for _, id := range []string{"a", "b", "c"} {
		p.AddOrUpdate(&Account{ID: id, Type: TypeQoder})
	}

	first := p.PickFor(DispatchOptions{Provider: TypeQoder, AffinityKey: "chat-1"})
	if first == nil {
		t.Fatal("no account selected")
	}
	for i := 0; i < 20; i++ {
		got := p.PickFor(DispatchOptions{Provider: TypeQoder, AffinityKey: "chat-1"})
		if got == nil || got.ID != first.ID {
			t.Fatalf("turn %d moved from %q to %v, breaking affinity", i, first.ID, got)
		}
	}

	// A different conversation is free to land elsewhere.
	other := p.PickFor(DispatchOptions{Provider: TypeQoder, AffinityKey: "chat-2"})
	if other == nil {
		t.Fatal("no account selected for the second conversation")
	}
}

// TestAffinityDropsWhenBoundAccountUnavailable checks a binding does not pin a
// conversation to an account that has since been parked.
func TestAffinityDropsWhenBoundAccountUnavailable(t *testing.T) {
	p := newAccountPoolWithSeed(5)
	a := &Account{ID: "a", Type: TypeQoder}
	b := &Account{ID: "b", Type: TypeQoder}
	p.AddOrUpdate(a)
	p.AddOrUpdate(b)

	first := p.PickFor(DispatchOptions{Provider: TypeQoder, AffinityKey: "chat"})
	if first == nil {
		t.Fatal("no account selected")
	}
	first.SetSoftCooldown(time.Hour, "429")

	got := p.PickFor(DispatchOptions{Provider: TypeQoder, AffinityKey: "chat"})
	if got == nil {
		t.Fatal("no account selected after the bound one was parked")
	}
	if got.ID == first.ID {
		t.Fatalf("still bound to the parked account %q", got.ID)
	}
}

// TestAffinityTableStaysBounded checks many distinct conversations cannot grow
// the binding table without limit.
func TestAffinityTableStaysBounded(t *testing.T) {
	p := newAccountPoolWithSeed(6)
	p.AddOrUpdate(&Account{ID: "a", Type: TypeQoder})

	for i := 0; i < affinityLimit*2; i++ {
		p.PickFor(DispatchOptions{Provider: TypeQoder, AffinityKey: string(rune('A'+i%26)) + itoaTest(i)})
	}

	p.affinity.mu.Lock()
	size := len(p.affinity.entries)
	p.affinity.mu.Unlock()

	if size > affinityLimit {
		t.Fatalf("affinity table holds %d entries, above the limit %d", size, affinityLimit)
	}
}

// TestPickFallsBackToRecoveringAccount checks a pool with nothing healthy still
// returns the account that recovers soonest, rather than nothing at all.
func TestPickFallsBackToRecoveringAccount(t *testing.T) {
	p := newAccountPoolWithSeed(7)

	late := &Account{ID: "late", Type: TypeQoder}
	late.SetSoftCooldown(time.Hour, "429")
	// Give it a plainly later recovery so the ordering is unambiguous.
	late.mu.Lock()
	late.CooldownUntil = time.Now().Add(2 * time.Hour)
	late.mu.Unlock()

	early := &Account{ID: "early", Type: TypeQoder}
	early.SetSoftCooldown(time.Minute, "429")
	early.mu.Lock()
	early.CooldownUntil = time.Now().Add(time.Minute)
	early.mu.Unlock()

	p.AddOrUpdate(late)
	p.AddOrUpdate(early)

	got := p.PickFor(DispatchOptions{Provider: TypeQoder})
	if got == nil {
		t.Fatal("expected a fallback account rather than nil")
	}
	if got.ID != "early" {
		t.Fatalf("selected %q, want the sooner-recovering account", got.ID)
	}
}

// TestPickSkipsDisabledOnFallback checks a disabled account is never returned as
// a fallback, since it cannot recover without intervention.
func TestPickSkipsDisabledOnFallback(t *testing.T) {
	p := newAccountPoolWithSeed(8)

	dead := &Account{ID: "dead", Type: TypeQoder}
	dead.Disable("bad credential")
	p.AddOrUpdate(dead)

	if got := p.PickFor(DispatchOptions{Provider: TypeQoder}); got != nil {
		t.Fatalf("selected a disabled account as a fallback: %q", got.ID)
	}
}

// TestPickForRespectsProvider checks provider isolation.
func TestPickForRespectsProvider(t *testing.T) {
	p := newAccountPoolWithSeed(9)
	p.AddOrUpdate(&Account{ID: "q", Type: TypeQoder})
	p.AddOrUpdate(&Account{ID: "w", Type: TypeWorkBuddy})

	got := p.PickFor(DispatchOptions{Provider: TypeWorkBuddy})
	if got == nil || got.ID != "w" {
		t.Fatalf("selected %v, want the WorkBuddy account", got)
	}
	if got := p.PickFor(DispatchOptions{Provider: TypeAntigravity}); got != nil {
		t.Fatalf("selected %q for a provider with no accounts", got.ID)
	}
}

func TestRecordAndReleaseDispatch(t *testing.T) {
	acc := &Account{ID: "a1"}
	acc.RecordDispatch()
	acc.RecordDispatch()
	if acc.InFlight != 2 {
		t.Fatalf("in-flight = %d, want 2", acc.InFlight)
	}
	acc.ReleaseDispatch()
	if acc.InFlight != 1 {
		t.Fatalf("in-flight = %d, want 1", acc.InFlight)
	}
	// Releasing more than recorded must not go negative.
	acc.ReleaseDispatch()
	acc.ReleaseDispatch()
	if acc.InFlight != 0 {
		t.Fatalf("in-flight = %d, want 0", acc.InFlight)
	}
}

// TestDispatchPrefersLessLoadedAccount checks in-flight work breaks ties, so a
// burst does not pile onto one account.
func TestDispatchPrefersLessLoadedAccount(t *testing.T) {
	p := newAccountPoolWithSeed(10)

	busy := &Account{ID: "busy", Type: TypeQoder, InFlight: 50}
	idle := &Account{ID: "idle", Type: TypeQoder, InFlight: 0}
	p.AddOrUpdate(busy)
	p.AddOrUpdate(idle)

	// With only two accounts the shortlist is both, but the ranking must put
	// the idle one first; over many picks it should dominate.
	counts := map[string]int{}
	for i := 0; i < 100; i++ {
		counts[p.PickFor(DispatchOptions{Provider: TypeQoder}).ID]++
	}
	if counts["idle"] <= counts["busy"] {
		t.Errorf("idle account was not preferred: %v", counts)
	}
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
