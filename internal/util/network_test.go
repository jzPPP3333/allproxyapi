package util

import (
	"net/http"
	"net/url"
	"testing"
)

func mustRequest(t *testing.T, rawURL string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	return req
}

func TestIsLoopbackHost(t *testing.T) {
	loopback := []string{
		"localhost",
		"app.localhost",
		"127.0.0.1",
		"127.1.2.3",
		"::1",
	}
	for _, host := range loopback {
		if !isLoopbackHost(host) {
			t.Errorf("isLoopbackHost(%q) = false, want true", host)
		}
	}

	routable := []string{
		"example.com",
		"api2-v2.qoder.sh",
		"10.0.0.1",
		"192.168.1.10",
		"",
	}
	for _, host := range routable {
		if isLoopbackHost(host) {
			t.Errorf("isLoopbackHost(%q) = true, want false", host)
		}
	}
}

// TestProxyBypassesLoopback verifies that a configured egress proxy is not
// applied to a loopback target, so a local upstream override stays reachable.
func TestProxyBypassesLoopback(t *testing.T) {
	proxyURL, err := url.Parse("http://127.0.0.1:10808")
	if err != nil {
		t.Fatalf("parse proxy: %v", err)
	}

	client := NewHTTPClient(proxyURL.String(), true, 0)

	loopbackReq, _ := url.Parse("http://localhost:8320/v1/models")
	if got, err := client.Transport.(*http.Transport).Proxy(
		mustRequest(t, loopbackReq.String()),
	); err != nil || got != nil {
		t.Fatalf("loopback target used proxy %v (err %v), want direct", got, err)
	}

	publicReq, _ := url.Parse("https://api2-v2.qoder.sh/model/v1/chat/completions")
	got, err := client.Transport.(*http.Transport).Proxy(mustRequest(t, publicReq.String()))
	if err != nil {
		t.Fatalf("proxy lookup: %v", err)
	}
	if got == nil {
		t.Fatal("public target did not use the configured proxy")
	}
	if got.Host != "127.0.0.1:10808" {
		t.Fatalf("public target used proxy %s, want 127.0.0.1:10808", got.Host)
	}
}
