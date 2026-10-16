package qoder

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jzPPP3333/allproxyapi/internal/config"
	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

// keyPairPEM generates a throwaway RSA key and returns both PEM encodings.
func keyPairPEM(t *testing.T) (pubPEM string, priv *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), key
}

// TestChatSealsRequestBodyWhenCOSYConfigured is the end-to-end proof that a
// configured public key actually changes what goes on the wire: the server
// receives a COSY envelope, not the plain request JSON.
func TestChatSealsRequestBodyWhenCOSYConfigured(t *testing.T) {
	pubPEM, priv := keyPairPEM(t)

	var received []byte
	var cosyHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		cosyHeader = r.Header.Get("X-Cosy-Alg")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[]}\n\n"))
	}))
	defer srv.Close()

	client := NewClient(config.QoderConfig{
		Region:  "intl",
		BaseURL: srv.URL,
		COSY:    config.COSYConfig{PublicKeyPEM: pubPEM},
	})

	acc := &pool.Account{
		ID:          "q-1",
		AccountName: "user@example.com",
		AccessToken: "pt-smoke",
	}

	reqBody := []byte(`{"model":"ultimate","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	resp, err := client.Chat(context.Background(), acc, reqBody)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	resp.Body.Close()

	// The body must be a COSY envelope, so it must not be the plain request.
	if strings.Contains(string(received), `"messages"`) {
		t.Fatalf("request body leaked plaintext: %s", truncate(received, 200))
	}

	// It must decode as an envelope and name the agreed cipher.
	env, err := Decode(string(received), &Config{Alphabet: MustAlphabet()})
	if err != nil {
		t.Fatalf("body is not a COSY envelope: %v (body: %s)", err, truncate(received, 200))
	}
	if env.Algorithm != CipherAlg {
		t.Fatalf("envelope alg = %q, want %q", env.Algorithm, CipherAlg)
	}
	if env.Wrapped == "" || env.IV == "" || env.Payload == "" {
		t.Fatalf("envelope is missing required fields: %+v", env)
	}
	if cosyHeader != CipherAlg {
		t.Fatalf("X-Cosy-Alg = %q, want %q", cosyHeader, CipherAlg)
	}

	// The payload must actually decrypt back to the original request.
	plain, err := Open(string(received), &Config{
		PublicKey:  &priv.PublicKey,
		PrivateKey: priv,
		Alphabet:   MustAlphabet(),
	})
	if err != nil {
		t.Fatalf("decrypt sealed request: %v", err)
	}

	var got map[string]interface{}
	if err := json.Unmarshal(plain, &got); err != nil {
		t.Fatalf("decrypted payload is not JSON: %v", err)
	}
	if got["model"] != "ultimate" {
		t.Fatalf("decrypted model = %v, want ultimate", got["model"])
	}
	if _, ok := got["metadata"]; !ok {
		t.Fatal("decrypted payload lost the metadata block")
	}
}

// TestChatSendsPlainBodyWithoutCOSY guards the default: with no key the request
// must be ordinary JSON so the public inference host stays reachable.
func TestChatSendsPlainBodyWithoutCOSY(t *testing.T) {
	var received []byte
	var cosyHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		cosyHeader = r.Header.Get("X-Cosy-Alg")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[]}\n\n"))
	}))
	defer srv.Close()

	client := NewClient(config.QoderConfig{Region: "intl", BaseURL: srv.URL})
	acc := &pool.Account{ID: "q-1", AccountName: "user@example.com", AccessToken: "pt-smoke"}

	resp, err := client.Chat(context.Background(), acc, []byte(`{"model":"ultimate","messages":[],"stream":true}`))
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	resp.Body.Close()

	if !strings.Contains(string(received), `"messages"`) {
		t.Fatalf("plaintext request was not forwarded as JSON: %s", truncate(received, 200))
	}
	if cosyHeader != "" {
		t.Fatalf("X-Cosy-Alg was set without a configured key: %q", cosyHeader)
	}
}

// TestChatDerivesStableFingerprintHeaders verifies the derived machine identity
// reaches the upstream and does not change between calls.
func TestChatDerivesStableFingerprintHeaders(t *testing.T) {
	var machineID, machineToken string
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		machineID = r.Header.Get("X-Machine-Id")
		machineToken = r.Header.Get("X-Machine-Token")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[]}\n\n"))
	}))
	defer srv.Close()

	client := NewClient(config.QoderConfig{Region: "intl", BaseURL: srv.URL})
	acc := &pool.Account{ID: "q-1", AccountName: "user@example.com", AccessToken: "pt-smoke"}

	for i := 0; i < 2; i++ {
		resp, err := client.Chat(context.Background(), acc, []byte(`{"model":"ultimate","messages":[]}`))
		if err != nil {
			t.Fatalf("chat %d: %v", i, err)
		}
		resp.Body.Close()
	}

	if calls != 2 {
		t.Fatalf("expected 2 upstream calls, got %d", calls)
	}
	if machineID == "" {
		t.Fatal("X-Machine-Id was not sent")
	}
	if machineToken == "" {
		t.Fatal("X-Machine-Token was not sent")
	}
	if machineID != DeriveMachineID(acc) {
		t.Fatalf("X-Machine-Id %q is not the derived value %q", machineID, DeriveMachineID(acc))
	}
}

// TestBuildCOSYConfigRejectsBadPEM proves an unusable key disables the envelope
// rather than producing a client that sends unreadable requests.
func TestBuildCOSYConfigRejectsBadPEM(t *testing.T) {
	if cfg := buildCOSYConfig(config.COSYConfig{PublicKeyPEM: "not a pem"}); cfg != nil {
		t.Fatal("an invalid public key did not disable COSY")
	}
	if cfg := buildCOSYConfig(config.COSYConfig{}); cfg != nil {
		t.Fatal("an empty public key did not disable COSY")
	}

	pubPEM, _ := keyPairPEM(t)
	cfg := buildCOSYConfig(config.COSYConfig{PublicKeyPEM: pubPEM})
	if cfg == nil || !cfg.Enabled() {
		t.Fatal("a valid public key did not enable COSY")
	}
	if cfg.keyAlg() != KeyAlgOAEP {
		t.Fatalf("default key alg = %q, want %q", cfg.keyAlg(), KeyAlgOAEP)
	}
}

func TestBuildCOSYConfigHonorsOverrides(t *testing.T) {
	pubPEM, _ := keyPairPEM(t)
	cfg := buildCOSYConfig(config.COSYConfig{
		PublicKeyPEM: pubPEM,
		KeyAlg:       KeyAlgPKCS1v,
		SignKey:      "hmac-key",
	})
	if cfg == nil {
		t.Fatal("config was nil")
	}
	if cfg.keyAlg() != KeyAlgPKCS1v {
		t.Fatalf("key alg = %q", cfg.keyAlg())
	}
	if len(cfg.SignKey) == 0 {
		t.Fatal("sign key was not applied")
	}

	// An invalid alphabet must fall back to the default rather than disabling
	// the whole envelope.
	cfg = buildCOSYConfig(config.COSYConfig{PublicKeyPEM: pubPEM, Alphabet: "short"})
	if cfg == nil || cfg.Alphabet.Dictionary() != DefaultAlphabet {
		t.Fatal("an invalid alphabet did not fall back to the default")
	}
}
