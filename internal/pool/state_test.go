package pool

import (
	"testing"
	"time"
)

func TestAccountStateTransitions(t *testing.T) {
	acc := &Account{ID: "a1", Type: TypeQoder}

	if got := acc.State(); got != StateActive {
		t.Fatalf("new account state = %q, want active", got)
	}

	// Soft cooldown: temporary.
	acc.SetSoftCooldown(0, "429")
	if got := acc.State(); got != StateSoftCooldown {
		t.Fatalf("after soft cooldown = %q, want soft_cooldown", got)
	}
	if acc.AvailableFor("m") {
		t.Error("a soft-cooled account reported available")
	}

	// A successful refresh clears it.
	acc.SetTokens("tok", "", time.Now().Add(time.Hour))
	if got := acc.State(); got != StateActive {
		t.Fatalf("after refresh = %q, want active", got)
	}

	// Hard cooldown: until the daily reset.
	acc.SetHardCooldown("quota")
	if got := acc.State(); got != StateHardCooldown {
		t.Fatalf("after hard cooldown = %q, want hard_cooldown", got)
	}

	// A hard cooldown must survive a token refresh: a good token does not
	// restore an exhausted allowance.
	acc.SetTokens("tok2", "", time.Now().Add(time.Hour))
	if got := acc.State(); got != StateHardCooldown {
		t.Fatalf("refresh cleared a hard cooldown: %q", got)
	}

	// Disabled: does not recover on its own.
	acc.Disable("bad credential")
	if got := acc.State(); got != StateDisabled {
		t.Fatalf("after disable = %q, want disabled", got)
	}
	if acc.AvailableFor("m") {
		t.Error("a disabled account reported available")
	}

	acc.Reenable()
	if got := acc.State(); got != StateActive {
		t.Fatalf("after re-enable = %q, want active", got)
	}
}

// TestSoftCooldownBacksOff checks consecutive failures lengthen the window
// rather than retrying at a fixed rate.
func TestSoftCooldownBacksOff(t *testing.T) {
	acc := &Account{ID: "a1"}

	acc.SetSoftCooldown(0, "429")
	first := acc.recoveryTime()

	acc.SetSoftCooldown(0, "429")
	second := acc.recoveryTime()

	if !second.After(first) {
		t.Fatalf("second cooldown (%v) did not extend the first (%v)", second, first)
	}
}

// TestSoftCooldownIsCapped checks the backoff cannot run away.
func TestSoftCooldownIsCapped(t *testing.T) {
	acc := &Account{ID: "a1"}
	for i := 0; i < 20; i++ {
		acc.SetSoftCooldown(0, "429")
	}
	if d := time.Until(acc.recoveryTime()); d > SoftCooldownMax+time.Minute {
		t.Fatalf("cooldown %v exceeds the cap %v", d, SoftCooldownMax)
	}
}

// TestRequestedDurationActsAsFloor checks a provider that knows its own retry
// window can extend the backoff but not shorten it below the derived value.
func TestRequestedDurationActsAsFloor(t *testing.T) {
	acc := &Account{ID: "a1"}
	acc.SetSoftCooldown(45*time.Minute, "429")
	if d := time.Until(acc.recoveryTime()); d < 44*time.Minute {
		t.Fatalf("requested duration ignored: %v", d)
	}

	// A shorter request must not undercut the derived backoff.
	acc2 := &Account{ID: "a2"}
	for i := 0; i < 4; i++ {
		acc2.SetSoftCooldown(0, "429")
	}
	acc2.SetSoftCooldown(time.Second, "429")
	if d := time.Until(acc2.recoveryTime()); d < SoftCooldownBase {
		t.Fatalf("a short request shortened the backoff to %v", d)
	}
}

// TestHardCooldownTargetsDailyReset is the key difference from a soft cooldown:
// a quota freeze ends on the provider's schedule, not after a fixed window.
func TestHardCooldownTargetsDailyReset(t *testing.T) {
	acc := &Account{ID: "a1"}
	acc.SetHardCooldown("quota exhausted")

	until := acc.recoveryTime()
	local := until.In(ResetLocation)
	if local.Hour() != DailyResetHour {
		t.Errorf("hard cooldown lifts at %02d:00, want %02d:00", local.Hour(), DailyResetHour)
	}
	if !until.After(time.Now()) {
		t.Error("hard cooldown lifts in the past")
	}
	if d := time.Until(until); d > 25*time.Hour {
		t.Errorf("hard cooldown is %v away, longer than a day", d)
	}
}

// TestNextDailyResetRollsForward checks the reset lands tomorrow when the hour
// has already passed today.
func TestNextDailyResetRollsForward(t *testing.T) {
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, ResetLocation)
	next := nextDailyReset(now)
	want := time.Date(2026, 10, 17, DailyResetHour, 0, 0, 0, ResetLocation)
	if !next.Equal(want) {
		t.Fatalf("nextDailyReset = %v, want %v", next, want)
	}

	// Before the hour, it is still today.
	early := time.Date(2026, 10, 16, 1, 0, 0, 0, ResetLocation)
	if got := nextDailyReset(early); got.Day() != 16 {
		t.Fatalf("nextDailyReset from 01:00 = %v, want same day", got)
	}
}

// TestHardCooldownOutranksSoft checks a rate limit cannot shorten a quota
// freeze.
func TestHardCooldownOutranksSoft(t *testing.T) {
	acc := &Account{ID: "a1"}
	acc.SetHardCooldown("quota")
	hard := acc.recoveryTime()

	acc.SetSoftCooldown(time.Minute, "429")
	if got := acc.recoveryTime(); !got.Equal(hard) {
		t.Fatalf("a soft cooldown moved the recovery from %v to %v", hard, got)
	}
	if acc.State() != StateHardCooldown {
		t.Fatalf("state = %q, want hard_cooldown", acc.State())
	}
}

// TestModelCooldownIsScoped is the per-model isolation behaviour: limiting one
// model must not remove the account for the others.
func TestModelCooldownIsScoped(t *testing.T) {
	acc := &Account{ID: "a1"}

	acc.SetModelCooldown("deepseek-r1", 10*time.Minute, "429")

	if acc.AvailableFor("deepseek-r1") {
		t.Error("the rate-limited model reported available")
	}
	if !acc.AvailableFor("deepseek-v3") {
		t.Error("an unrelated model was blocked by another model's cooldown")
	}
	// The account as a whole is still usable.
	if acc.State() != StateActive {
		t.Errorf("account state = %q, want active", acc.State())
	}
}

// TestModelCooldownExpires checks the scope lifts.
func TestModelCooldownExpires(t *testing.T) {
	acc := &Account{ID: "a1"}
	acc.SetModelCooldown("m", time.Millisecond, "429")
	time.Sleep(5 * time.Millisecond)
	if !acc.AvailableFor("m") {
		t.Error("an expired model cooldown still blocked the model")
	}
}

// TestSetModelCooldownWithoutModelFallsBack checks an unknown model name does
// not silently produce a no-op.
func TestSetModelCooldownWithoutModelFallsBack(t *testing.T) {
	acc := &Account{ID: "a1"}
	acc.SetModelCooldown("", time.Minute, "429")
	if acc.AvailableFor("") {
		t.Error("an empty model name produced no cooldown at all")
	}
}

// TestModelCooldownMapStaysBounded checks retention does not accumulate an entry
// per model ever seen.
func TestModelCooldownMapStaysBounded(t *testing.T) {
	acc := &Account{ID: "a1"}

	// Seed an entry that is long expired beyond the retention window.
	acc.mu.Lock()
	acc.modelCooldowns = map[string]time.Time{
		"ancient": time.Now().Add(-(modelCooldownRetention + time.Hour)),
	}
	acc.mu.Unlock()

	acc.SetModelCooldown("current", time.Minute, "429")

	acc.mu.RLock()
	_, ancientKept := acc.modelCooldowns["ancient"]
	size := len(acc.modelCooldowns)
	acc.mu.RUnlock()

	if ancientKept {
		t.Error("a long-expired model cooldown was retained forever")
	}
	if size != 1 {
		t.Errorf("model cooldown map has %d entries, want 1", size)
	}
}

// TestResetCooldownClearsEverything checks the manual reset path.
func TestResetCooldownClearsEverything(t *testing.T) {
	acc := &Account{ID: "a1"}
	acc.SetHardCooldown("quota")
	acc.SetModelCooldown("m", time.Hour, "429")
	acc.Disable("bad")

	acc.ResetCooldown()

	if acc.State() != StateActive {
		t.Fatalf("state = %q, want active", acc.State())
	}
	if !acc.AvailableFor("m") {
		t.Error("a model cooldown survived a reset")
	}
}

// TestSnapshotReportsState covers the dashboard read path.
func TestSnapshotReportsState(t *testing.T) {
	acc := &Account{ID: "a1"}
	acc.SetHardCooldown("quota exhausted")

	snap := acc.Snapshot()
	if snap.State != StateHardCooldown {
		t.Errorf("state = %q", snap.State)
	}
	if snap.Reason != "quota exhausted" {
		t.Errorf("reason = %q", snap.Reason)
	}
	if snap.CooldownUntil.IsZero() {
		t.Error("snapshot did not report when the account recovers")
	}

	acc.SetModelCooldown("m", time.Hour, "429")
	snap = acc.Snapshot()
	if len(snap.ModelCooldowns) != 1 {
		t.Errorf("model cooldowns = %v, want one entry", snap.ModelCooldowns)
	}
}

// TestCloneCarriesLifecycleState checks a cloned account reports the same
// availability, since the dashboard reads clones.
func TestCloneCarriesLifecycleState(t *testing.T) {
	acc := &Account{ID: "a1", Type: TypeQoder}
	acc.SetHardCooldown("quota")
	acc.SetModelCooldown("m", time.Hour, "429")

	c := acc.Clone()
	if c.State() != StateHardCooldown {
		t.Errorf("clone state = %q, want hard_cooldown", c.State())
	}
	if c.AvailableFor("m") {
		t.Error("clone lost the model cooldown")
	}

	// Mutating the clone must not affect the original.
	c.modelCooldowns["other"] = time.Now().Add(time.Hour)
	if acc.AvailableFor("other") {
		t.Error("clone shares the model cooldown map with the original")
	}
}

func TestDisableAndReenable(t *testing.T) {
	acc := &Account{ID: "a1"}
	acc.Disable("credential rejected")
	if acc.State() != StateDisabled {
		t.Fatalf("state = %q", acc.State())
	}
	if acc.DisabledReason() == "" {
		t.Error("disabled reason not retained")
	}
	acc.Reenable()
	if acc.State() != StateActive || acc.DisabledReason() != "" {
		t.Errorf("re-enable left state=%q reason=%q", acc.State(), acc.DisabledReason())
	}
}
