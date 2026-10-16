package qoder

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strings"

	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

// The inference host treats X-Machine-Id as a stable install identity. A value
// that changes on every process start looks like a new device each time, which
// trips anti-abuse limits, so the value is derived deterministically from the
// account's identity and then persisted.

// FingerprintSalt namespaces the derivation so the machine id cannot collide
// with a request or session id derived from the same seed.
const FingerprintSalt = "qoder-machine"

// DeriveMachineID returns a stable RFC 4122 v4-shaped identifier for an
// account. It is formatted like a UUID so it is indistinguishable from the
// client-generated value the upstream expects, but every byte comes from a
// hash of the account identity and is therefore reproducible.
//
// Identity priority is the account's own name, then its UID, then its id, so a
// re-import of the same account keeps the same fingerprint.
func DeriveMachineID(acc *pool.Account) string {
	if acc == nil {
		return uuidFromSeed("qoder-anonymous")
	}
	for _, candidate := range []string{acc.AccountName, acc.UID, acc.Email, acc.Name, acc.ID} {
		if strings.TrimSpace(candidate) != "" {
			return uuidFromSeed(candidate)
		}
	}
	return uuidFromSeed("qoder-anonymous")
}

// DeriveMachineToken returns a deterministic opaque token alongside the
// machine id, so a re-imported account presents the same pair.
func DeriveMachineToken(acc *pool.Account) string {
	seed := "qoder-token"
	if acc != nil {
		for _, candidate := range []string{acc.AccountName, acc.UID, acc.Email, acc.Name, acc.ID} {
			if strings.TrimSpace(candidate) != "" {
				seed = "qoder-token:" + candidate
				break
			}
		}
	}
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// uuidFromSeed expands an arbitrary seed into a v4-shaped UUID using MD5.
// MD5 is used purely as a fixed-width mixer here; this is an identifier, not a
// security primitive, and matching the shape of a real client UUID is the goal.
func uuidFromSeed(seed string) string {
	sum := md5.Sum([]byte(FingerprintSalt + ":" + seed))
	b := sum[:16]

	// Pin the version and variant nibbles so the output parses as a v4 UUID.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// EnsureFingerprint fills an account's machine id and token if either is
// missing and persists the account when it changed, returning true in that
// case.
//
// Derivation is deterministic, so a re-derivation after a restart yields the
// same value; persisting just makes the identity explicit in the credential
// file and survives a change to the derivation inputs.
func EnsureFingerprint(acc *pool.Account) bool {
	if acc == nil {
		return false
	}
	changed := false
	if acc.MachineID == "" {
		acc.MachineID = DeriveMachineID(acc)
		changed = true
	}
	if acc.MachineToken == "" {
		acc.MachineToken = DeriveMachineToken(acc)
		changed = true
	}
	if changed && acc.FilePath != "" {
		if err := pool.SaveAccountToFile(acc); err != nil {
			log.Printf("[Qoder] 持久化账号 %s 的设备指纹失败: %v", acc.Name, err)
		}
	}
	return changed
}
