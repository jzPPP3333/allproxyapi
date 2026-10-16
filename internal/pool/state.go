package pool

import (
	"fmt"
	"sync"
	"time"
)

// AccountState is where an account sits in its lifecycle.
//
// The distinction that matters is why an account is unavailable, because that
// determines when it comes back. A rate limit clears in minutes; an exhausted
// daily allowance clears on the provider's reset; a dead credential does not
// clear at all.
type AccountState string

const (
	// StateActive is a usable account.
	StateActive AccountState = "active"
	// StateSoftCooldown is a temporary limit (rate limit or concurrency cap).
	// The account returns when the backoff expires.
	StateSoftCooldown AccountState = "soft_cooldown"
	// StateHardCooldown is an exhausted allowance. The account is parked until
	// the provider's daily reset rather than retried in the meantime.
	StateHardCooldown AccountState = "hard_cooldown"
	// StateDisabled is a credential that cannot be repaired automatically.
	// It stays out of dispatch until a human re-authenticates it.
	StateDisabled AccountState = "disabled"
)

// ResetLocation is the timezone the daily allowance reset is computed in. It
// is not necessarily the server's local zone; the providers reset on their own
// schedule.
var ResetLocation = time.FixedZone("CST", 8*3600)

// DailyResetHour is the hour, in ResetLocation, at which a hard cooldown
// expires. The providers replenish allowances overnight, so an account frozen
// on quota is parked until the next occurrence of this hour.
const DailyResetHour = 4

// nextDailyReset returns the next occurrence of the reset hour after now.
func nextDailyReset(now time.Time) time.Time {
	n := now.In(ResetLocation)
	reset := time.Date(n.Year(), n.Month(), n.Day(), DailyResetHour, 0, 0, 0, ResetLocation)
	if !reset.After(n) {
		reset = reset.AddDate(0, 0, 1)
	}
	return reset
}

// soft cooldown backoff bounds. A rate-limited account starts at the base and
// doubles per consecutive failure, capped, so a persistently-limited account
// backs off instead of hammering.
//
// These are the defaults; the operator can override them in config, so the
// live values are read through the setters below rather than used directly.
const (
	SoftCooldownBase = 10 * time.Minute
	SoftCooldownMax  = 2 * time.Hour
)

// cooldownPolicy holds the live backoff bounds. It is swappable at runtime
// because the config can be reloaded while requests are in flight.
var cooldownPolicy struct {
	sync.RWMutex
	base time.Duration
	max  time.Duration
}

func init() {
	cooldownPolicy.base = SoftCooldownBase
	cooldownPolicy.max = SoftCooldownMax
}

// SetCooldownPolicy overrides the backoff bounds. A zero value keeps the
// current setting, so a config that omits one field does not reset it.
func SetCooldownPolicy(base, max time.Duration) {
	cooldownPolicy.Lock()
	defer cooldownPolicy.Unlock()
	if base > 0 {
		cooldownPolicy.base = base
	}
	if max > 0 {
		if max < cooldownPolicy.base {
			// A cap below the base would make the first cooldown exceed it.
			max = cooldownPolicy.base
		}
		cooldownPolicy.max = max
	}
}

// CooldownPolicy returns the live bounds, for display and tests.
func CooldownPolicy() (base, max time.Duration) {
	cooldownPolicy.RLock()
	defer cooldownPolicy.RUnlock()
	return cooldownPolicy.base, cooldownPolicy.max
}

// modelCooldownRetention bounds the per-model cooldown map so a long-lived
// process cannot accumulate an entry per model ever seen.
const modelCooldownRetention = 6 * time.Hour

// AccountStateSnapshot is a consistent read of an account's availability.
type AccountStateSnapshot struct {
	State          AccountState         `json:"state"`
	Reason         string               `json:"reason,omitempty"`
	CooldownUntil  time.Time            `json:"cooldown_until,omitempty"`
	ModelCooldowns map[string]time.Time `json:"model_cooldowns,omitempty"`
	FailStreak     int                  `json:"fail_streak"`
	Disabled       bool                 `json:"disabled"`
}

// State reports the account's current lifecycle state.
func (a *Account) State() AccountState {
	s := a.Snapshot()
	if s.Disabled {
		return StateDisabled
	}
	return s.State
}

// Snapshot returns a consistent view of the account's availability.
func (a *Account) Snapshot() AccountStateSnapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()

	now := time.Now()
	snap := AccountStateSnapshot{
		Reason:     a.LastError,
		FailStreak: a.FailStreak,
		Disabled:   a.disabled,
	}

	if a.disabled {
		snap.State = StateDisabled
		return snap
	}

	switch {
	case !a.hardCooldownUntil.IsZero() && now.Before(a.hardCooldownUntil):
		snap.State = StateHardCooldown
		snap.CooldownUntil = a.hardCooldownUntil
	case now.Before(a.CooldownUntil):
		snap.State = StateSoftCooldown
		snap.CooldownUntil = a.CooldownUntil
	default:
		snap.State = StateActive
	}

	if len(a.modelCooldowns) > 0 {
		snap.ModelCooldowns = make(map[string]time.Time, len(a.modelCooldowns))
		for model, until := range a.modelCooldowns {
			if now.Before(until) {
				snap.ModelCooldowns[model] = until
			}
		}
	}
	return snap
}

// AvailableFor reports whether the account can serve a model right now.
//
// A soft cooldown is account-wide, but a per-model cooldown only blocks that
// model: a rate limit that only fires on one model should not remove the
// account from rotation for the others.
func (a *Account) AvailableFor(model string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.disabled {
		return false
	}
	now := time.Now()
	if !a.hardCooldownUntil.IsZero() && now.Before(a.hardCooldownUntil) {
		return false
	}
	if now.Before(a.CooldownUntil) {
		return false
	}
	if model != "" {
		if until, ok := a.modelCooldowns[model]; ok && now.Before(until) {
			return false
		}
	}
	return true
}

// SetSoftCooldown parks the account with exponential backoff.
//
// A caller-supplied duration is respected as a floor so a provider that knows
// its own retry window can state it; otherwise the backoff is derived from the
// consecutive failure count.
func (a *Account) SetSoftCooldown(requested time.Duration, reason string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// A hard freeze outranks a soft one. A rate limit arriving while the
	// account is out of quota must not shorten the quota wait, so the soft
	// cooldown is recorded but does not lift the freeze.
	if !a.hardCooldownUntil.IsZero() && time.Now().Before(a.hardCooldownUntil) {
		a.LastError = reason
		return
	}

	a.FailStreak++
	a.LastError = reason
	a.disabled = false

	base, ceiling := CooldownPolicy()

	dur := requested
	backoff := base * time.Duration(1<<min(a.FailStreak-1, 8))
	if backoff > ceiling {
		backoff = ceiling
	}
	if dur < backoff {
		dur = backoff
	}
	if dur > ceiling {
		dur = ceiling
	}

	until := time.Now().Add(dur)
	if until.After(a.CooldownUntil) {
		a.CooldownUntil = until
	}
}

// SetModelCooldown parks one model on this account, leaving the others usable.
func (a *Account) SetModelCooldown(model string, dur time.Duration, reason string) {
	if model == "" {
		a.SetSoftCooldown(dur, reason)
		return
	}
	if dur <= 0 {
		base, _ := CooldownPolicy()
		dur = base
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.modelCooldowns == nil {
		a.modelCooldowns = make(map[string]time.Time)
	}
	a.modelCooldowns[model] = time.Now().Add(dur)
	a.LastError = reason

	// Drop entries that have already expired so the map stays proportional to
	// the models currently limited rather than every model ever seen.
	if len(a.modelCooldowns) > 0 {
		now := time.Now()
		for m, until := range a.modelCooldowns {
			if now.Sub(until) > modelCooldownRetention {
				delete(a.modelCooldowns, m)
			}
		}
	}
}

// SetHardCooldown parks the account until the next daily allowance reset.
//
// This is deliberately not a fixed duration: the allowance refills on the
// provider's schedule, so retrying inside an arbitrary window would only
// re-send the same request into the same limit.
func (a *Account) SetHardCooldown(reason string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.FailStreak++
	a.LastError = reason
	a.disabled = false
	a.hardCooldownUntil = nextDailyReset(time.Now())
	a.CooldownUntil = time.Time{}
}

// Disable takes the account out of rotation until it is re-authenticated.
func (a *Account) Disable(reason string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.disabled = true
	a.LastError = reason
	a.CooldownUntil = time.Time{}
	a.hardCooldownUntil = time.Time{}
}

// Reenable returns a disabled account to rotation, used after a successful
// re-authentication.
func (a *Account) Reenable() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.disabled = false
	a.FailStreak = 0
	a.LastError = ""
	a.CooldownUntil = time.Time{}
	a.hardCooldownUntil = time.Time{}
}

// ClearModelCooldowns drops every per-model cooldown, used when an account is
// manually reset.
func (a *Account) ClearModelCooldowns() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.modelCooldowns = nil
}

// DisabledReason returns the reason an account was disabled, if it was.
func (a *Account) DisabledReason() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.disabled {
		return ""
	}
	return a.LastError
}

// StateLabel is the human-readable form used by the dashboard.
func StateLabel(s AccountState) string {
	switch s {
	case StateActive:
		return "正常"
	case StateSoftCooldown:
		return "软冷却"
	case StateHardCooldown:
		return "硬冷却"
	case StateDisabled:
		return "已失效"
	default:
		return fmt.Sprintf("未知(%s)", s)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
