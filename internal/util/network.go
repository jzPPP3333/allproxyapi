package util

import (
	"crypto/tls"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// DetectProxyURL attempts to find a usable HTTP/SOCKS proxy URL from:
// 1. Explicit user override
// 2. Standard environment variables (HTTPS_PROXY, HTTP_PROXY, ALL_PROXY)
// 3. Windows Internet Settings registry (ProxyServer when ProxyEnable=1)
func DetectProxyURL(configured string) string {
	if strings.TrimSpace(configured) != "" {
		return strings.TrimSpace(configured)
	}

	// 2. Environment variables
	for _, env := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		if val := os.Getenv(env); strings.TrimSpace(val) != "" {
			return strings.TrimSpace(val)
		}
	}

	// 3. Windows registry lookup via reg query
	out, err := exec.Command("reg", "query", "HKCU\\Software\\Microsoft\\Windows\\CurrentVersion\\Internet Settings").CombinedOutput()
	if err == nil {
		lines := strings.Split(string(out), "\n")
		proxyEnabled := false
		proxyServer := ""

		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.Contains(line, "ProxyEnable") && strings.Contains(line, "0x1") {
				proxyEnabled = true
			}
			if strings.Contains(line, "ProxyServer") {
				parts := strings.Fields(line)
				if len(parts) >= 3 {
					proxyServer = parts[len(parts)-1]
				}
			}
		}

		if proxyEnabled && proxyServer != "" {
			// ProxyServer may be "http=127.0.0.1:10808;https=127.0.0.1:10808" or "127.0.0.1:10808"
			if !strings.Contains(proxyServer, "://") {
				proxyServer = "http://" + proxyServer
			}
			log.Printf("[Network] Auto-detected Windows system proxy: %s", proxyServer)
			return proxyServer
		}
	}

	return ""
}

// isLoopbackHost reports whether a host resolves to the local machine by name.
// It does not perform DNS resolution, so a name that happens to point at
// loopback still uses the proxy.
func isLoopbackHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// NewHTTPClient creates an HTTP client with proxy and optional HTTP/1.1 enforcement.
func NewHTTPClient(proxyStr string, forceHTTP1 bool, timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}

	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 15 * time.Second,
	}

	if proxyStr != "" {
		if parsed, err := url.Parse(proxyStr); err == nil {
			proxyFn := http.ProxyURL(parsed)
			// The egress proxy exists to reach public upstreams. Sending a
			// loopback request through it is never correct, and it breaks any
			// local upstream override.
			transport.Proxy = func(req *http.Request) (*url.URL, error) {
				if isLoopbackHost(req.URL.Hostname()) {
					return nil, nil
				}
				return proxyFn(req)
			}
		}
	} else {
		transport.Proxy = http.ProxyFromEnvironment
	}

	if forceHTTP1 {
		// Enforce HTTP/1.1 to match native client TLS fingerprint without ALPN h2
		transport.ForceAttemptHTTP2 = false
		transport.TLSNextProto = make(map[string]func(authority string, c *tls.Conn) http.RoundTripper)
	}

	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}
}
