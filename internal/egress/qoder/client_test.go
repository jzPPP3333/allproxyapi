package qoder

import (
	"testing"

	"github.com/jzPPP3333/allproxyapi/internal/config"
)

// configForTest is the baseline config shared by the package's tests.
func configForTest() config.QoderConfig {
	return config.QoderConfig{Region: "intl", ClientVersion: "1.1.49"}
}

// TestNormalizeModel covers the pass-through contract.
//
// Qoder exposes tier presets, not named models, so normalization must not
// rewrite a model name onto a tier: doing so would silently serve a different
// model than the caller asked for. Only an empty request gets a default.
func TestNormalizeModel(t *testing.T) {
	cases := map[string]string{
		"":            "auto",
		"   ":         "auto",
		"auto":        "auto",
		"ultimate":    "ultimate",
		"performance": "performance",
		"efficient":   "efficient",
		"lite":        "lite",
		// A name that is not a tier is passed through unchanged rather than
		// being mapped onto an unrelated tier.
		"qwen-max":            "qwen-max",
		"deepseek-chat":       "deepseek-chat",
		"kimi-k3":             "kimi-k3",
		"glm-5.3":             "glm-5.3",
		"minimax-m3":          "minimax-m3",
		"some-unmapped-model": "some-unmapped-model",
		// Surrounding whitespace is trimmed.
		"  ultimate  ": "ultimate",
	}
	for in, want := range cases {
		if got := NormalizeModel(in); got != want {
			t.Errorf("NormalizeModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRegionEndpoints(t *testing.T) {
	if got := resolveBaseURL("intl", ""); got != "https://api2-v2.qoder.sh" {
		t.Errorf("intl base url = %q", got)
	}
	if got := resolveBaseURL("cn", ""); got != "https://gateway.qoder.com.cn" {
		t.Errorf("cn base url = %q", got)
	}
	// An explicit override always wins.
	if got := resolveBaseURL("cn", "https://example.test"); got != "https://example.test" {
		t.Errorf("override base url = %q", got)
	}
	if got := resolveAuthURL("cn", ""); got != "https://openapi.qoder.com.cn" {
		t.Errorf("cn auth url = %q", got)
	}
	// Unknown regions fall back to intl.
	if got := regionOf("nonsense"); got != "intl" {
		t.Errorf("regionOf(nonsense) = %q, want intl", got)
	}
}

func TestDefaultModelIDsNonEmpty(t *testing.T) {
	c := NewClient(config.QoderConfig{Region: "intl"})
	ids := c.DefaultModelIDs()
	if len(ids) == 0 {
		t.Fatal("expected a non-empty default model catalog")
	}
	seen := make(map[string]bool)
	for _, id := range ids {
		if id == "" {
			t.Error("empty model id in catalog")
		}
		if seen[id] {
			t.Errorf("duplicate model id %q in catalog", id)
		}
		seen[id] = true
	}
}
