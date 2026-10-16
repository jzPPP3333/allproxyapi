package pool

import (
	"testing"
	"time"
)

// restorePolicy puts the defaults back so one test cannot leak into another.
func restorePolicy(t *testing.T) {
	t.Helper()
	SetCooldownPolicy(SoftCooldownBase, SoftCooldownMax)
	t.Cleanup(func() { SetCooldownPolicy(SoftCooldownBase, SoftCooldownMax) })
}

// TestCooldownPolicyOverride checks a configured backoff actually applies,
// rather than the constants being the only reality.
func TestCooldownPolicyOverride(t *testing.T) {
	restorePolicy(t)

	SetCooldownPolicy(2*time.Minute, 30*time.Minute)
	base, max := CooldownPolicy()
	if base != 2*time.Minute || max != 30*time.Minute {
		t.Fatalf("policy = %v/%v, want 2m/30m", base, max)
	}

	acc := &Account{ID: "a1"}
	acc.SetSoftCooldown(0, "429")

	// The first cooldown must honour the configured base, not the default.
	d := time.Until(acc.recoveryTime())
	if d > 2*time.Minute+time.Second {
		t.Errorf("first cooldown = %v, want about the configured base 2m", d)
	}
	if d < time.Minute {
		t.Errorf("first cooldown = %v, shorter than expected", d)
	}
}

// TestCooldownPolicyCapsBackoff checks the configured ceiling is respected
// instead of the built-in one.
func TestCooldownPolicyCapsBackoff(t *testing.T) {
	restorePolicy(t)

	SetCooldownPolicy(time.Minute, 5*time.Minute)

	acc := &Account{ID: "a1"}
	for i := 0; i < 12; i++ {
		acc.SetSoftCooldown(0, "429")
	}
	if d := time.Until(acc.recoveryTime()); d > 5*time.Minute+time.Second {
		t.Errorf("backoff = %v, above the configured cap of 5m", d)
	}
}

// TestCooldownPolicyIgnoresZeros checks a config that sets only one bound does
// not reset the other.
func TestCooldownPolicyIgnoresZeros(t *testing.T) {
	restorePolicy(t)

	SetCooldownPolicy(3*time.Minute, 40*time.Minute)
	SetCooldownPolicy(0, 0)

	base, max := CooldownPolicy()
	if base != 3*time.Minute || max != 40*time.Minute {
		t.Fatalf("a zero-valued update changed the policy to %v/%v", base, max)
	}
}

// TestCooldownPolicyClampsInvertedBounds checks a cap below the base is raised,
// since otherwise the first cooldown would exceed the ceiling.
func TestCooldownPolicyClampsInvertedBounds(t *testing.T) {
	restorePolicy(t)

	SetCooldownPolicy(30*time.Minute, time.Minute)

	base, max := CooldownPolicy()
	if max < base {
		t.Fatalf("cap %v is below the base %v", max, base)
	}
}

// TestCooldownPolicyIsRaceFree exercises concurrent reads and writes, since the
// config can be reloaded while requests are in flight.
func TestCooldownPolicyIsRaceFree(t *testing.T) {
	restorePolicy(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			SetCooldownPolicy(time.Duration(i+1)*time.Second, time.Hour)
		}
	}()

	acc := &Account{ID: "a1"}
	for i := 0; i < 200; i++ {
		acc.SetSoftCooldown(0, "429")
		_ = acc.AvailableFor("m")
	}
	<-done
}
