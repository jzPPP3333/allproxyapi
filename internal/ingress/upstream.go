package ingress

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/jzPPP3333/allproxyapi/internal/ingress/openai"
	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

// ResponseFormat describes how an upstream frames a chat response, so the
// ingress layer knows which converter to apply without knowing the vendor.
type ResponseFormat int

const (
	// FormatOpenAI is a bare OpenAI-compatible SSE stream (data: {...} / data: [DONE]).
	FormatOpenAI ResponseFormat = iota
	// FormatGemini is a Gemini SSE stream whose payloads are wrapped in an
	// outer envelope that must be unwrapped before conversion.
	FormatGemini
)

// Upstream is one egress provider. An adapter owns everything vendor-specific:
// auth headers, wire envelope, model naming, and how its response is framed.
// Adding a provider means adding one adapter and one registry entry; the
// router itself stays vendor-free.
type Upstream interface {
	// ID is the routing identifier, matching pool.AccountType.
	ID() pool.AccountType
	// Label is the human-readable provider name for logs and the dashboard.
	Label() string
	// Format reports how this provider frames chat responses.
	Format() ResponseFormat
	// DefaultModels is the fallback catalog used when an account has no
	// discovered models. It must not be presented as live upstream state.
	DefaultModels() []string
	// FetchModels discovers the live model catalog for an account. Providers
	// with no discovery endpoint should return their static catalog.
	FetchModels(ctx context.Context, acc *pool.Account) ([]pool.ModelMetadata, error)
	// Chat sends one normalized request upstream and returns the raw response.
	// It must not write to the client.
	Chat(ctx context.Context, acc *pool.Account, req *openai.ChatCompletionRequest, rawBody []byte) (*http.Response, error)
	// Unwrap strips any provider envelope from the response body. Adapters
	// whose format is FormatOpenAI return src unchanged.
	Unwrap(src io.Reader) (io.Reader, error)
	// Checkin performs the provider's daily check-in for one account. A
	// provider with no check-in concept returns a CheckinUnsupported result
	// rather than an error, so a batch run reports it plainly.
	Checkin(ctx context.Context, acc *pool.Account) (pool.CheckinResult, error)
}

// Registry maps provider IDs to their adapters.
type Registry struct {
	byID  map[pool.AccountType]Upstream
	order []pool.AccountType
}

func NewRegistry() *Registry {
	return &Registry{byID: make(map[pool.AccountType]Upstream)}
}

func (r *Registry) Register(u Upstream) {
	id := u.ID()
	if _, dup := r.byID[id]; !dup {
		r.order = append(r.order, id)
	}
	r.byID[id] = u
}

func (r *Registry) Get(id pool.AccountType) (Upstream, bool) {
	u, ok := r.byID[id]
	return u, ok
}

func (r *Registry) GetByName(name string) (Upstream, bool) {
	return r.Get(pool.AccountType(strings.ToLower(strings.TrimSpace(name))))
}

// All returns the registered adapters in registration order.
func (r *Registry) All() []Upstream {
	out := make([]Upstream, 0, len(r.order))
	for _, id := range r.order {
		if u, ok := r.byID[id]; ok {
			out = append(out, u)
		}
	}
	return out
}

// IDs returns the known provider identifiers, sorted.
func (r *Registry) IDs() []string {
	ids := make([]string, 0, len(r.order))
	for _, id := range r.order {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	return ids
}

// Resolve maps a public model name to a provider and the upstream model name.
//
// Resolution order: an explicit "<provider>/<model>" prefix, then the
// configured regex rules in order, then the configured default provider.
func (s *Server) ResolveProvider(model string) (provider string, upstreamModel string) {
	lower := strings.ToLower(model)

	if idx := strings.Index(lower, "/"); idx > 0 {
		if _, ok := s.upstreams.GetByName(lower[:idx]); ok {
			return lower[:idx], model[idx+1:]
		}
	}

	for _, rule := range s.cfg.Routing.ModelRules {
		matched, _ := regexp.MatchString(rule.Pattern, lower)
		if matched {
			target := model
			if rule.UpstreamModel != "" {
				target = rule.UpstreamModel
			}
			return strings.ToLower(rule.Provider), target
		}
	}

	fallback := strings.ToLower(s.cfg.Routing.DefaultProvider)
	if _, ok := s.upstreams.GetByName(fallback); !ok {
		if ids := s.upstreams.IDs(); len(ids) > 0 {
			fallback = ids[0]
		}
	}
	return fallback, model
}

// callUpstream selects an account for the provider and calls it, failing over
// across accounts. The returned response body is owned by the caller.
func (s *Server) callUpstream(ctx context.Context, provider string, req *openai.ChatCompletionRequest, rawBody []byte) (*http.Response, Upstream, error) {
	up, ok := s.upstreams.GetByName(provider)
	if !ok {
		return nil, nil, fmt.Errorf("unknown provider %q (known: %s)", provider, strings.Join(s.upstreams.IDs(), ", "))
	}

	const maxAttempts = 3
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		acc := s.pool.Pick(up.ID())
		if acc == nil {
			if lastErr != nil {
				return nil, up, lastErr
			}
			return nil, up, fmt.Errorf("no available %s accounts in pool", up.Label())
		}

		resp, err := up.Chat(ctx, acc, req, rawBody)
		if err != nil {
			lastErr = err
			log.Printf("[%s] 账号 %s 失败: %v, 故障转移重试 (%d/%d)", up.Label(), acc.Name, err, attempt+1, maxAttempts)
			continue
		}
		return resp, up, nil
	}

	return nil, up, fmt.Errorf("all %s accounts failed: %w", up.Label(), lastErr)
}

// forwardToOpenAI relays an upstream response to the client in OpenAI dialect.
func (s *Server) forwardToOpenAI(w http.ResponseWriter, up Upstream, resp *http.Response, model string, stream bool) {
	defer resp.Body.Close()

	switch up.Format() {
	case FormatGemini:
		pr, err := up.Unwrap(resp.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("unwrap upstream stream failed: %v", err), http.StatusBadGateway)
			return
		}
		if stream {
			_ = StreamGeminiToOpenAI(pr, w, model)
			return
		}
		s.aggregateGeminiSSEToOpenAI(w, pr, model)

	default: // FormatOpenAI
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			flusher, _ := w.(http.Flusher)

			buf := make([]byte, 4096)
			for {
				n, readErr := resp.Body.Read(buf)
				if n > 0 {
					if _, werr := w.Write(buf[:n]); werr != nil {
						return
					}
					if flusher != nil {
						flusher.Flush()
					}
				}
				if readErr != nil {
					return
				}
			}
		}
		s.aggregateOpenAISSEToJSON(w, resp.Body, model)
	}
}

// forwardToAnthropic relays an upstream response to the client in Anthropic dialect.
func (s *Server) forwardToAnthropic(w http.ResponseWriter, up Upstream, resp *http.Response, model string, stream bool) {
	defer resp.Body.Close()

	switch up.Format() {
	case FormatGemini:
		pr, err := up.Unwrap(resp.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("unwrap upstream stream failed: %v", err), http.StatusBadGateway)
			return
		}
		if stream {
			oaiPr, oaiPw := io.Pipe()
			go func() {
				_ = StreamGeminiToOpenAI(pr, newStreamCaptureWriter(oaiPw), model)
				_ = oaiPw.Close()
			}()
			_ = StreamOpenAIToAnthropic(oaiPr, w, model)
			return
		}
		s.aggregateGeminiSSEToAnthropic(w, pr, model)

	default: // FormatOpenAI
		if stream {
			_ = StreamOpenAIToAnthropic(resp.Body, w, model)
			return
		}
		s.aggregateOpenAISSEToAnthropic(w, resp.Body, model)
	}
}

// newStreamCaptureWriter adapts an io.Writer to the http.ResponseWriter shape
// that the stream converters expect.
func newStreamCaptureWriter(w io.Writer) *streamCaptureWriter {
	return &streamCaptureWriter{w: w}
}
