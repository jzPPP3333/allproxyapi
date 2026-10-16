package qoder

import (
	"testing"

	"github.com/jzPPP3333/allproxyapi/internal/config"
)

// configForTest is the baseline config shared by the package's tests.
func configForTest() config.QoderConfig {
	return config.QoderConfig{Region: "intl", ClientVersion: "1.1.49"}
}

func TestNormalizeModel(t *testing.T) {
	cases := map[string]string{
		"":                    "ultimate",
		"ultimate":            "ultimate",
		"qmodel_38max":        "qmodel_38max",
		"Qwen3.8-Max":         "qmodel_38max",
		"qwen-flash":          "qfmodel",
		"qwen-max":            "qmodel_38max",
		"deepseek-chat":       "dmodel",
		"deepseek-v4-flash":   "dfmodel",
		"kimi-k2-code":        "kmodel",
		"kimi-k3":             "kmodel_latest",
		"glm-5.3-flash":       "gfmodel",
		"glm-5.3":             "gmodel",
		"minimax-m3":          "mmodel",
		"some-unmapped-model": "some-unmapped-model",
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
