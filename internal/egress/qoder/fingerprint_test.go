package qoder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestDeriveMachineIDIsStable(t *testing.T) {
	acc := &pool.Account{ID: "a1", UID: "1000xyz", AccountName: "user@example.com"}

	first := DeriveMachineID(acc)
	second := DeriveMachineID(acc)
	if first != second {
		t.Fatalf("machine id is not stable: %q != %q", first, second)
	}

	// A fresh Account with the same identity (re-import) must match.
	reimported := &pool.Account{ID: "different-id", UID: "1000xyz", AccountName: "user@example.com"}
	if got := DeriveMachineID(reimported); got != first {
		t.Fatalf("re-import changed the fingerprint: %q != %q", got, first)
	}

	if !uuidShape.MatchString(first) {
		t.Fatalf("machine id %q is not v4-shaped", first)
	}
}

func TestDeriveMachineIDSeparatesAccounts(t *testing.T) {
	a := DeriveMachineID(&pool.Account{AccountName: "alice@example.com"})
	b := DeriveMachineID(&pool.Account{AccountName: "bob@example.com"})
	if a == b {
		t.Fatal("distinct accounts produced the same machine id")
	}
}

func TestDeriveMachineIDFallsBackThroughIdentityFields(t *testing.T) {
	// AccountName wins, then UID, then Email, then Name, then ID.
	byName := DeriveMachineID(&pool.Account{ID: "x", Name: "display", AccountName: "primary"})
	byNameOnly := DeriveMachineID(&pool.Account{AccountName: "primary"})
	if byName != byNameOnly {
		t.Fatal("AccountName was not the preferred identity field")
	}

	byUID := DeriveMachineID(&pool.Account{UID: "u-1"})
	byUIDOnly := DeriveMachineID(&pool.Account{ID: "ignored", UID: "u-1"})
	if byUID != byUIDOnly {
		t.Fatal("UID was not preferred over ID")
	}

	// An account with no identity at all still yields a usable stable value.
	empty := DeriveMachineID(&pool.Account{})
	if !uuidShape.MatchString(empty) {
		t.Fatalf("anonymous fingerprint %q is not v4-shaped", empty)
	}
	if DeriveMachineID(nil) != empty {
		t.Fatal("nil account did not match the anonymous fingerprint")
	}
}

func TestDeriveMachineTokenIsStableAndDistinct(t *testing.T) {
	acc := &pool.Account{AccountName: "user@example.com"}
	token := DeriveMachineToken(acc)
	if token == "" {
		t.Fatal("machine token is empty")
	}
	if token != DeriveMachineToken(&pool.Account{AccountName: "user@example.com"}) {
		t.Fatal("machine token is not stable")
	}
	if token == DeriveMachineToken(&pool.Account{AccountName: "other@example.com"}) {
		t.Fatal("distinct accounts produced the same machine token")
	}
	// The token must not be the machine id under another name.
	if token == DeriveMachineID(acc) {
		t.Fatal("machine token collides with the machine id")
	}
}

func TestEnsureFingerprintFillsOnlyMissingFields(t *testing.T) {
	acc := &pool.Account{AccountName: "user@example.com"}
	if !EnsureFingerprint(acc) {
		t.Fatal("expected a modified account on first fill")
	}
	if acc.MachineID == "" || acc.MachineToken == "" {
		t.Fatal("fingerprint fields were not populated")
	}

	id, token := acc.MachineID, acc.MachineToken
	if EnsureFingerprint(acc) {
		t.Fatal("expected no modification when the fingerprint is already present")
	}
	if acc.MachineID != id || acc.MachineToken != token {
		t.Fatal("an existing fingerprint was overwritten")
	}

	// A partially populated account gets only the missing half.
	partial := &pool.Account{AccountName: "user@example.com", MachineID: id}
	if !EnsureFingerprint(partial) {
		t.Fatal("expected the missing token to be filled")
	}
	if partial.MachineID != id {
		t.Fatal("an existing machine id was overwritten")
	}
}

func TestEnsureFingerprintHandlesNil(t *testing.T) {
	if EnsureFingerprint(nil) {
		t.Fatal("nil account reported a modification")
	}
}

func TestEnsureFingerprintPersistsToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "qoder-account.json")

	acc := &pool.Account{
		ID:          "q-persist",
		Type:        pool.TypeQoder,
		Name:        "persist",
		AccountName: "persist@example.com",
		AccessToken: "pt-abc",
		FilePath:    path,
	}

	if !EnsureFingerprint(acc) {
		t.Fatal("expected the fingerprint to be derived")
	}

	// The identity must be written back so it survives a restart.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted account: %v", err)
	}
	var persisted struct {
		MachineID    string `json:"machine_id"`
		MachineToken string `json:"machine_token"`
	}
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("decode persisted account: %v", err)
	}
	if persisted.MachineID != acc.MachineID {
		t.Fatalf("persisted machine_id = %q, want %q", persisted.MachineID, acc.MachineID)
	}
	if persisted.MachineToken != acc.MachineToken {
		t.Fatalf("persisted machine_token = %q, want %q", persisted.MachineToken, acc.MachineToken)
	}
}

func TestEnsureFingerprintWithoutFilePathDoesNotFail(t *testing.T) {
	// An in-memory account (imported through the API) has no file to write.
	acc := &pool.Account{ID: "q-mem", AccountName: "mem@example.com"}
	if !EnsureFingerprint(acc) {
		t.Fatal("expected the fingerprint to be derived")
	}
	if acc.MachineID == "" {
		t.Fatal("machine id was not derived for an in-memory account")
	}
}
