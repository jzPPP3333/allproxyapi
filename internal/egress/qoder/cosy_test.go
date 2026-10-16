package qoder

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) (*rsa.PrivateKey, *rsa.PublicKey) {
	t.Helper()
	// 2048 bits keeps the test fast while leaving room for OAEP-SHA256 under a
	// 16-byte session key.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key, &key.PublicKey
}

func TestSealOpenRoundTrip(t *testing.T) {
	priv, pub := testKey(t)
	cfg := &Config{
		PublicKey:  pub,
		PrivateKey: priv,
		SignKey:    []byte("test-sign-key"),
		Alphabet:   MustAlphabet(),
	}

	const message = `{"model":"ultimate","messages":[{"role":"user","content":"你好, Qoder"}]}`
	wire, err := Seal([]byte(message), cfg)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	out, err := Open(wire, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(out) != message {
		t.Fatalf("round trip mismatch:\n got %q\nwant %q", out, message)
	}
}

func TestOpenRejectsTamperedPayload(t *testing.T) {
	priv, pub := testKey(t)
	cfg := &Config{PublicKey: pub, PrivateKey: priv, SignKey: []byte("k"), Alphabet: MustAlphabet()}

	wire, err := Seal([]byte("original"), cfg)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	env, err := Decode(wire, cfg)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Flip one character of the ciphertext.
	payload := []byte(env.Payload)
	payload[0] = 'A' + (payload[0]-'A'+1)%26
	env.Payload = string(payload)

	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	tampered := MustAlphabet().Encode(body)

	if _, err := Open(tampered, cfg); err == nil {
		t.Fatal("expected tampered payload to be rejected")
	}
}

func TestSealProducesDistinctCiphertexts(t *testing.T) {
	priv, pub := testKey(t)
	cfg := &Config{PublicKey: pub, PrivateKey: priv, Alphabet: MustAlphabet()}

	first, err := Seal([]byte("same plaintext"), cfg)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	second, err := Seal([]byte("same plaintext"), cfg)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	// A fresh session key and IV per call means identical inputs must not
	// produce identical wire bytes.
	if first == second {
		t.Fatal("sealing the same plaintext twice produced identical output")
	}
}

func TestSealRequiresPublicKey(t *testing.T) {
	if _, err := Seal([]byte("x"), &Config{}); err != ErrNoPublicKey {
		t.Fatalf("expected ErrNoPublicKey, got %v", err)
	}
	if _, err := Seal([]byte("x"), nil); err != ErrNoPublicKey {
		t.Fatalf("expected ErrNoPublicKey for nil config, got %v", err)
	}
}

func TestAlphabetIsNotStandardBase64(t *testing.T) {
	alpha := MustAlphabet()

	// A payload that produces characters differing between the two alphabets.
	wire := alpha.Encode([]byte("test payload for alphabet divergence"))
	if strings.ContainsAny(wire, "+/=") {
		t.Fatalf("COSY alphabet leaked standard base64 characters: %q", wire)
	}

	decoded, err := alpha.Decode(wire)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(decoded) != "test payload for alphabet divergence" {
		t.Fatalf("alphabet round trip mismatch: %q", decoded)
	}
}

func TestNewAlphabetRejectsBadDictionaries(t *testing.T) {
	cases := map[string]string{
		"too short":    "abc",
		"too long":     strings.Repeat("a", 65),
		"duplicate":    strings.Repeat("a", 64),
		"with newline": "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_\n",
		"non-ascii":    strings.Repeat("é", 64),
	}
	for name, dict := range cases {
		if _, err := NewAlphabet(dict); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestPKCS7PaddingRoundTrip(t *testing.T) {
	for size := 0; size < 48; size++ {
		data := make([]byte, size)
		for i := range data {
			data[i] = byte(i)
		}
		padded := pkcs7Pad(data, 16)
		if len(padded)%16 != 0 {
			t.Fatalf("size %d: padded length %d not block-aligned", size, len(padded))
		}
		out, err := pkcs7Unpad(padded, 16)
		if err != nil {
			t.Fatalf("size %d: unpad: %v", size, err)
		}
		if len(out) != size {
			t.Fatalf("size %d: got %d bytes back", size, len(out))
		}
	}
}

func TestPKCS1v15KeyWrap(t *testing.T) {
	priv, pub := testKey(t)
	cfg := &Config{
		PublicKey:  pub,
		PrivateKey: priv,
		KeyAlg:     KeyAlgPKCS1v,
		Alphabet:   MustAlphabet(),
	}
	wire, err := Seal([]byte("pkcs1v15 path"), cfg)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	out, err := Open(wire, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(out) != "pkcs1v15 path" {
		t.Fatalf("round trip mismatch: %q", out)
	}
}

func TestDecodeRejectsUnknownVersion(t *testing.T) {
	body, err := json.Marshal(Envelope{Version: 99, Algorithm: CipherAlg})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	wire := MustAlphabet().Encode(body)

	if _, err := Decode(wire, nil); err == nil {
		t.Fatal("expected unsupported version to be rejected")
	}
}

func TestParsePublicKeyPEMFormats(t *testing.T) {
	_, pub := testKey(t)

	pkix, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal pkix: %v", err)
	}
	pkixPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pkix})
	if _, err := ParsePublicKeyPEM(pkixPEM); err != nil {
		t.Fatalf("parse PKIX: %v", err)
	}

	pkcs1 := x509.MarshalPKCS1PublicKey(pub)
	pkcs1PEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: pkcs1})
	if _, err := ParsePublicKeyPEM(pkcs1PEM); err != nil {
		t.Fatalf("parse PKCS1: %v", err)
	}

	if _, err := ParsePublicKeyPEM([]byte("not a pem")); err == nil {
		t.Fatal("expected non-PEM input to be rejected")
	}
}

func TestSignCoversTimestamp(t *testing.T) {
	key := []byte("signing-key")
	iv := []byte("0123456789abcdef")
	payload := []byte("ciphertext")

	a := sign(key, iv, payload, 1000)
	b := sign(key, iv, payload, 1001)
	if string(a) == string(b) {
		t.Fatal("signature did not change when the timestamp changed")
	}

	// The signing clock must be injectable so a fixed timestamp is reproducible.
	fixed := time.Unix(1700000000, 0)
	cfg := &Config{Now: func() time.Time { return fixed }}
	if cfg.now() != fixed {
		t.Fatal("injected clock was not used")
	}
}
