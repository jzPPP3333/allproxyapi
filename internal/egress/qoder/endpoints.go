package qoder

import (
	"fmt"
	"net/http"
	"time"

	"github.com/jzPPP3333/allproxyapi/internal/util"
)

// Endpoint pairs per region. The inference path exposes an
// OpenAI-compatible /model/v1/chat/completions route that needs no
// request signing; auth covers PAT exchange and refresh.
var regions = map[string]struct {
	BaseURL string
	AuthURL string
}{
	"intl": {
		BaseURL: "https://api2-v2.qoder.sh",
		AuthURL: "https://openapi.qoder.sh",
	},
	"cn": {
		BaseURL: "https://gateway.qoder.com.cn",
		AuthURL: "https://openapi.qoder.com.cn",
	},
}

// DefaultRegion is used when an account does not name one.
const DefaultRegion = "intl"

// ChatPath is the OpenAI-compatible inference route.
const ChatPath = "/model/v1/chat/completions"

// ClientIDs are the OAuth client identifiers used by the Qoder clients.
var ClientIDs = map[string]string{
	"intl": "732aef47-9cf2-46a2-95fe-4cebb5d0d1fa",
	"cn":   "e883ade2-e6e3-4d6d-adf7-f92ceff5fdcb",
}

func regionOf(region string) string {
	if region == "cn" {
		return "cn"
	}
	return "intl"
}

func resolveBaseURL(region, override string) string {
	if override != "" {
		return override
	}
	return regions[regionOf(region)].BaseURL
}

func resolveAuthURL(region, override string) string {
	if override != "" {
		return override
	}
	return regions[regionOf(region)].AuthURL
}

// newHTTPClient builds a client honoring an optional egress proxy.
func newHTTPClient(proxyURL string, timeout time.Duration) *http.Client {
	return util.NewHTTPClient(util.DetectProxyURL(proxyURL), true, timeout)
}

func wrapErr(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("qoder %s: %w", what, err)
}
