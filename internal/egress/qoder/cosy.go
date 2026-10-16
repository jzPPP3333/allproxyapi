package qoder

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
)

// COSY is Qoder's request envelope: a fresh AES-128 session key wraps the
// payload, that key is itself wrapped with the server RSA public key, and the
// finished envelope travels as a dictionary-encoded string.
//
// Layout on the wire (before dictionary encoding) is JSON:
//
//	{"v":1,"alg":"AES-128-CBC","key_alg":"RSA-OAEP-SHA256",
//	 "ek":"<rsa(aesKey)>","iv":"<iv>","data":"<ciphertext>",
//	 "ts":<unix>,"nonce":"<hex>","sig":"<hmac>"}
//
// Every field marked base64 above is encoded with the COSY alphabet, which is
// a permutation of the standard one and therefore a separate decoder.

// Key wrap modes for the session key.
const (
	KeyAlgOAEP   = "RSA-OAEP-SHA256"
	KeyAlgPKCS1v = "RSA-PKCS1v15"
)

// CipherAlg is the only payload cipher COSY uses.
const CipherAlg = "AES-128-CBC"

// EnvelopeVersion is the current COSY envelope revision.
const EnvelopeVersion = 1

// DefaultAlphabet is the COSY base64 dictionary. It is a permutation of the
// standard alphabet, so a standard decoder rejects COSY output and vice versa.
const DefaultAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

// ErrNoPublicKey reports an attempt to seal without a server public key.
var ErrNoPublicKey = errors.New("cosy: no RSA public key configured")

// Alphabet is a base64 dictionary. The zero value is unusable; construct one
// with NewAlphabet.
type Alphabet struct {
	enc *base64.Encoding
	raw string
}

// NewAlphabet validates a 64-character dictionary and returns a codec for it.
// An empty dictionary selects DefaultAlphabet.
func NewAlphabet(dict string) (*Alphabet, error) {
	if dict == "" {
		dict = DefaultAlphabet
	}
	if len(dict) != 64 {
		return nil, fmt.Errorf("cosy: alphabet must be 64 characters, got %d", len(dict))
	}
	seen := make(map[rune]bool, 64)
	for _, r := range dict {
		if r > 0x7f {
			return nil, fmt.Errorf("cosy: alphabet must be ASCII, found %q", r)
		}
		if seen[r] {
			return nil, fmt.Errorf("cosy: alphabet repeats %q", r)
		}
		seen[r] = true
	}
	if strings.ContainsAny(dict, "\r\n\t ") {
		return nil, errors.New("cosy: alphabet must not contain whitespace")
	}
	return &Alphabet{enc: base64.NewEncoding(dict), raw: dict}, nil
}

// MustAlphabet is NewAlphabet for the default dictionary; it cannot fail.
func MustAlphabet() *Alphabet {
	a, err := NewAlphabet(DefaultAlphabet)
	if err != nil {
		panic(err)
	}
	return a
}

// Encode renders bytes with the COSY dictionary.
func (a *Alphabet) Encode(b []byte) string { return a.enc.EncodeToString(b) }

// Decode parses a COSY-dictionary string back to bytes.
func (a *Alphabet) Decode(s string) ([]byte, error) { return a.enc.DecodeString(s) }

// Dictionary returns the raw dictionary.
func (a *Alphabet) Dictionary() string { return a.raw }

// Envelope is the decoded COSY request container.
type Envelope struct {
	Version   int    `json:"v"`
	Algorithm string `json:"alg"`
	KeyAlg    string `json:"key_alg"`
	Wrapped   string `json:"ek"`
	IV        string `json:"iv"`
	Payload   string `json:"data"`
	Timestamp int64  `json:"ts"`
	Nonce     string `json:"nonce"`
	Signature string `json:"sig"`
}

// Config carries the key material and dictionary a COSY session needs. The
// public key is required to seal; the private key is only needed to open a
// server response, which the public inference host does not use.
type Config struct {
	PublicKey  *rsa.PublicKey
	PrivateKey *rsa.PrivateKey
	SignKey    []byte
	Alphabet   *Alphabet
	KeyAlg     string

	// Now is injectable for deterministic tests. Nil means time.Now.
	Now func() time.Time
}

func (c *Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Config) alphabet() *Alphabet {
	if c.Alphabet != nil {
		return c.Alphabet
	}
	return MustAlphabet()
}

func (c *Config) keyAlg() string {
	if c.KeyAlg != "" {
		return c.KeyAlg
	}
	return KeyAlgOAEP
}

// Enabled reports whether this Config can seal a request.
func (c *Config) Enabled() bool { return c != nil && c.PublicKey != nil }

// Seal encrypts plaintext into a COSY wire string.
func Seal(plaintext []byte, cfg *Config) (string, error) {
	if cfg == nil || cfg.PublicKey == nil {
		return "", ErrNoPublicKey
	}

	key := make([]byte, 16) // AES-128
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("cosy: read session key: %w", err)
	}
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		return "", fmt.Errorf("cosy: read iv: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("cosy: new cipher: %w", err)
	}
	padded := pkcs7Pad(plaintext, aes.BlockSize)
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)

	wrapped, err := wrapKey(key, cfg.PublicKey, cfg.keyAlg())
	if err != nil {
		return "", err
	}

	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("cosy: read nonce: %w", err)
	}

	alpha := cfg.alphabet()
	env := Envelope{
		Version:   EnvelopeVersion,
		Algorithm: CipherAlg,
		KeyAlg:    cfg.keyAlg(),
		Wrapped:   alpha.Encode(wrapped),
		IV:        alpha.Encode(iv),
		Payload:   alpha.Encode(ciphertext),
		Timestamp: cfg.now().Unix(),
		Nonce:     hexString(nonce),
	}

	// The signature commits to the ciphertext, the IV and the timestamp so a
	// replayed or mutated envelope fails to verify.
	if len(cfg.SignKey) > 0 {
		env.Signature = alpha.Encode(sign(cfg.SignKey, iv, ciphertext, env.Timestamp))
	}

	body, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("cosy: marshal envelope: %w", err)
	}
	// The whole envelope is dictionary-encoded for transport, so a standard
	// base64 decoder sees an unreadable blob.
	return alpha.Encode(body), nil
}

// Open reverses Seal. It is used for a server response that echoes the same
// envelope, which only happens when the private key is configured.
func Open(wire string, cfg *Config) ([]byte, error) {
	env, err := Decode(wire, cfg)
	if err != nil {
		return nil, err
	}
	if cfg == nil || cfg.PrivateKey == nil {
		return nil, errors.New("cosy: no RSA private key configured")
	}

	alpha := cfg.alphabet()
	wrapped, err := alpha.Decode(env.Wrapped)
	if err != nil {
		return nil, fmt.Errorf("cosy: decode wrapped key: %w", err)
	}
	key, err := unwrapKey(wrapped, cfg.PrivateKey, env.KeyAlg)
	if err != nil {
		return nil, err
	}

	iv, err := alpha.Decode(env.IV)
	if err != nil {
		return nil, fmt.Errorf("cosy: decode iv: %w", err)
	}
	ciphertext, err := alpha.Decode(env.Payload)
	if err != nil {
		return nil, fmt.Errorf("cosy: decode payload: %w", err)
	}

	if len(cfg.SignKey) > 0 {
		got, sigErr := alpha.Decode(env.Signature)
		if sigErr != nil {
			return nil, fmt.Errorf("cosy: decode signature: %w", sigErr)
		}
		want := sign(cfg.SignKey, iv, ciphertext, env.Timestamp)
		if !hmac.Equal(got, want) {
			return nil, errors.New("cosy: signature mismatch")
		}
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("cosy: new cipher: %w", err)
	}
	if len(iv) != aes.BlockSize {
		return nil, fmt.Errorf("cosy: iv must be %d bytes, got %d", aes.BlockSize, len(iv))
	}
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("cosy: ciphertext length %d is not a multiple of the block size", len(ciphertext))
	}

	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ciphertext)

	out, err := pkcs7Unpad(plain, aes.BlockSize)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Decode parses a COSY wire string into its envelope without decrypting, so a
// caller can inspect the algorithm and timestamp first.
func Decode(wire string, cfg *Config) (*Envelope, error) {
	alpha := MustAlphabet()
	if cfg != nil {
		alpha = cfg.alphabet()
	}

	body, err := alpha.Decode(strings.TrimSpace(wire))
	if err != nil {
		return nil, fmt.Errorf("cosy: decode envelope: %w", err)
	}

	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("cosy: parse envelope: %w", err)
	}
	if env.Version != EnvelopeVersion {
		return nil, fmt.Errorf("cosy: unsupported envelope version %d", env.Version)
	}
	if env.Algorithm != CipherAlg {
		return nil, fmt.Errorf("cosy: unsupported cipher %q", env.Algorithm)
	}
	return &env, nil
}

// wrapKey encrypts the AES session key to the server public key.
func wrapKey(key []byte, pub *rsa.PublicKey, alg string) ([]byte, error) {
	switch alg {
	case KeyAlgPKCS1v:
		out, err := rsa.EncryptPKCS1v15(rand.Reader, pub, key)
		if err != nil {
			return nil, fmt.Errorf("cosy: wrap key (pkcs1v15): %w", err)
		}
		return out, nil
	case KeyAlgOAEP, "":
		out, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, key, nil)
		if err != nil {
			return nil, fmt.Errorf("cosy: wrap key (oaep): %w", err)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("cosy: unknown key algorithm %q", alg)
	}
}

func unwrapKey(wrapped []byte, priv *rsa.PrivateKey, alg string) ([]byte, error) {
	switch alg {
	case KeyAlgPKCS1v:
		key, err := rsa.DecryptPKCS1v15(rand.Reader, priv, wrapped)
		if err != nil {
			return nil, fmt.Errorf("cosy: unwrap key (pkcs1v15): %w", err)
		}
		return key, nil
	case KeyAlgOAEP, "":
		key, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, priv, wrapped, nil)
		if err != nil {
			return nil, fmt.Errorf("cosy: unwrap key (oaep): %w", err)
		}
		return key, nil
	default:
		return nil, fmt.Errorf("cosy: unknown key algorithm %q", alg)
	}
}

// sign produces the envelope MAC over iv || ciphertext || timestamp.
func sign(key, iv, ciphertext []byte, ts int64) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(iv)
	mac.Write(ciphertext)
	var stamp [8]byte
	for i := 7; i >= 0; i-- {
		stamp[i] = byte(ts)
		ts >>= 8
	}
	mac.Write(stamp[:])
	return mac.Sum(nil)
}

func hexString(b []byte) string { return hex.EncodeToString(b) }

// pkcs7Pad appends PKCS#7 padding to a full block boundary.
func pkcs7Pad(data []byte, blockSize int) []byte {
	n := blockSize - len(data)%blockSize
	return append(data, bytes.Repeat([]byte{byte(n)}, n)...)
}

// pkcs7Unpad removes and validates PKCS#7 padding.
func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errors.New("cosy: invalid padded length")
	}
	n := int(data[len(data)-1])
	if n == 0 || n > blockSize {
		return nil, errors.New("cosy: invalid padding")
	}
	if n > len(data) {
		return nil, errors.New("cosy: padding exceeds payload")
	}
	for _, b := range data[len(data)-n:] {
		if int(b) != n {
			return nil, errors.New("cosy: inconsistent padding")
		}
	}
	return data[:len(data)-n], nil
}

// ParsePublicKeyPEM decodes an RSA public key from PKIX ("PUBLIC KEY") or
// PKCS#1 ("RSA PUBLIC KEY") PEM.
func ParsePublicKeyPEM(pemData []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, errors.New("cosy: no PEM block in public key")
	}
	if key, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		rsaKey, ok := key.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("cosy: public key is not RSA")
		}
		return rsaKey, nil
	}
	rsaKey, err := x509.ParsePKCS1PublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("cosy: parse public key: %w", err)
	}
	return rsaKey, nil
}

// ParsePrivateKeyPEM decodes an RSA private key from PKCS#1 or PKCS#8 PEM.
func ParsePrivateKeyPEM(pemData []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, errors.New("cosy: no PEM block in private key")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("cosy: parse private key: %w", err)
	}
	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("cosy: private key is not RSA")
	}
	return rsaKey, nil
}
