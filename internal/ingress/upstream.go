package ingress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/jzPPP3333/allproxyapi/internal/config"
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
	// ModelCatalog is the same set as DefaultModels carrying its metadata, so
	// the dashboard can show real capability flags instead of inferring them
	// from the model name.
	ModelCatalog() []pool.ModelMetadata
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
// The lookup chain, highest priority first:
//
//  1. an explicit "<provider>/<model>" prefix on the request
//  2. an entry in routing.model_mapping, the operator's alias table
//  3. the configured regex rules, in order
//  4. the configured default provider
//
// A mapping value of "<provider>/<model>" pins both; a bare name overrides only
// the upstream model and lets the rules choose the provider.
func (s *Server) ResolveProvider(model string) (provider string, upstreamModel string) {
	lower := strings.ToLower(model)

	// 1. An explicit prefix always wins: the caller is being specific.
	if idx := strings.Index(lower, "/"); idx > 0 {
		if _, ok := s.upstreams.GetByName(lower[:idx]); ok {
			return lower[:idx], model[idx+1:]
		}
	}

	// 2. The operator's alias table.
	if mapped, ok := s.lookupAlias(lower); ok {
		target := mapped.model
		switch {
		case mapped.provider != "":
			return mapped.provider, target
		default:
			// Only the model was overridden; the rules pick the provider, using
			// the alias target so a rule matching the upstream name still works.
			if p, resolved := s.resolveByRules(target); p != "" {
				return p, resolved
			}
			fallback := s.defaultProvider()
			return fallback, target
		}
	}

	// 3. Rules, then 4. the default provider.
	if p, resolved := s.resolveByRules(model); p != "" {
		return p, resolved
	}
	return s.defaultProvider(), model
}

// aliasTarget is a resolved routing.model_mapping entry.
type aliasTarget struct {
	provider string
	model    string
}

// lookupAlias consults the alias table, matching the requested name
// case-insensitively. Keys are normalized at lookup time so an operator does
// not have to remember which case a model name uses.
func (s *Server) lookupAlias(lower string) (aliasTarget, bool) {
	mapping := s.routing()
	if len(mapping.ModelMapping) == 0 {
		return aliasTarget{}, false
	}

	raw, ok := mapping.ModelMapping[lower]
	if !ok {
		return aliasTarget{}, false
	}
	value := strings.TrimSpace(raw)
	if value == "" {
		return aliasTarget{}, false
	}

	// "<provider>/<model>" pins the provider too.
	if idx := strings.Index(value, "/"); idx > 0 {
		if _, known := s.upstreams.GetByName(strings.ToLower(value[:idx])); known {
			return aliasTarget{
				provider: strings.ToLower(value[:idx]),
				model:    value[idx+1:],
			}, true
		}
	}
	return aliasTarget{model: value}, true
}

// resolveByRules applies the configured regex rules in order. It returns an
// empty provider when nothing matched.
func (s *Server) resolveByRules(model string) (string, string) {
	lower := strings.ToLower(model)
	for _, rule := range s.routing().ModelRules {
		matched, err := regexp.MatchString(rule.Pattern, lower)
		if err != nil {
			continue
		}
		if matched {
			target := model
			if rule.UpstreamModel != "" {
				target = rule.UpstreamModel
			}
			return strings.ToLower(rule.Provider), target
		}
	}
	return "", ""
}

// defaultProvider returns the configured default, falling back to the first
// registered provider so an unset or stale value still routes somewhere.
func (s *Server) defaultProvider() string {
	fallback := strings.ToLower(s.routing().DefaultProvider)
	if _, ok := s.upstreams.GetByName(fallback); !ok {
		if ids := s.upstreams.IDs(); len(ids) > 0 {
			fallback = ids[0]
		}
	}
	return fallback
}

// routing reads the routing config under the reload lock, since the config can
// be swapped while requests are in flight.
func (s *Server) routing() config.RoutingConfig {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg.Routing
}

// callUpstream selects an account for the provider and calls it, failing over
// across accounts. The returned response body is owned by the caller.
//
// Selection is delegated to the pool's dispatcher, which applies the health
// filter, model capability matching and the value-weighted choice. Each failed
// attempt parks the account it used, so the next attempt is a genuinely
// different account rather than a retry of the same one.
func (s *Server) callUpstream(ctx context.Context, provider string, req *openai.ChatCompletionRequest, rawBody []byte) (*http.Response, Upstream, error) {
	up, ok := s.upstreams.GetByName(provider)
	if !ok {
		return nil, nil, fmt.Errorf("unknown provider %q (known: %s)", provider, strings.Join(s.upstreams.IDs(), ", "))
	}

	const maxAttempts = 3
	var lastErr error
	var lastAcc *pool.Account

	for attempt := 0; attempt < maxAttempts; attempt++ {
		acc := s.pool.PickFor(pool.DispatchOptions{
			Provider: up.ID(),
			Model:    req.Model,
			// A conversation sticks to one account so multi-turn requests are
			// not spread across accounts, which would both hurt continuity and
			// look like unrelated accounts sending similar contexts.
			AffinityKey: sessionKey(req),
		})
		if acc == nil {
			if lastErr != nil {
				return nil, up, lastErr
			}
			return nil, up, fmt.Errorf("no available %s accounts in pool", up.Label())
		}
		// Retrying the account that just failed would be a wasted attempt.
		if lastAcc != nil && acc.ID == lastAcc.ID {
			acc = s.pool.PickFor(pool.DispatchOptions{Provider: up.ID(), Model: req.Model})
			if acc == nil || acc.ID == lastAcc.ID {
				break
			}
		}

		acc.RecordDispatch()
		resp, err := up.Chat(ctx, acc, req, rawBody)
		acc.ReleaseDispatch()

		// Attribution covers the failing case too: an operator diagnosing a
		// failure needs the account that failed, and `all accounts failed`
		// alone does not say which ones were tried.
		if info := requestInfoFrom(ctx); info != nil {
			info.Account = acc.Name
		}

		if err != nil {
			lastErr, lastAcc = err, acc
			log.Printf("[%s] 账号 %s 失败: %v, 故障转移重试 (%d/%d)", up.Label(), acc.Name, err, attempt+1, maxAttempts)
			continue
		}
		return resp, up, nil
	}

	return nil, up, fmt.Errorf("all %s accounts failed: %w", up.Label(), lastErr)
}

// sessionKey derives a stable per-conversation key for affinity.
//
// The first user message is used because it is stable across the turns of one
// conversation and differs between conversations. A request with no messages
// gets no affinity, which is correct: there is nothing to stay consistent with.
func sessionKey(req *openai.ChatCompletionRequest) string {
	if req == nil || len(req.Messages) == 0 {
		return ""
	}
	for _, m := range req.Messages {
		if m.Role != "user" {
			continue
		}
		if text, ok := m.Content.(string); ok && text != "" {
			sum := sha256.Sum256([]byte(text))
			return hex.EncodeToString(sum[:16])
		}
	}
	return ""
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
