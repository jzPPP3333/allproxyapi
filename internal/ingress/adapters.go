package ingress

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/jzPPP3333/allproxyapi/internal/egress/antigravity"
	"github.com/jzPPP3333/allproxyapi/internal/egress/qoder"
	"github.com/jzPPP3333/allproxyapi/internal/egress/workbuddy"
	"github.com/jzPPP3333/allproxyapi/internal/ingress/openai"
	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

// ---------------------------------------------------------------------------
// Google Antigravity (Cloud Code PA)
//
// The upstream speaks Gemini. Requests are translated into the Gemini schema
// and wrapped in the Cloud Code PA envelope; the response stream carries the
// same envelope and is unwrapped before conversion back to the client dialect.
// ---------------------------------------------------------------------------

type antigravityAdapter struct {
	client *antigravity.Client
}

// NewAntigravityAdapter wraps the Antigravity client as an upstream adapter.
func NewAntigravityAdapter(c *antigravity.Client) Upstream { return &antigravityAdapter{client: c} }

func (a *antigravityAdapter) ID() pool.AccountType { return pool.TypeAntigravity }
func (a *antigravityAdapter) Label() string        { return "Antigravity" }
func (a *antigravityAdapter) Format() ResponseFormat {
	return FormatGemini
}

func (a *antigravityAdapter) DefaultModels() []string {
	return a.client.GetDefaultModels()
}

func (a *antigravityAdapter) FetchModels(ctx context.Context, acc *pool.Account) ([]pool.ModelMetadata, error) {
	return a.client.FetchModels(ctx, acc)
}

func (a *antigravityAdapter) Chat(ctx context.Context, acc *pool.Account, req *openai.ChatCompletionRequest, _ []byte) (*http.Response, error) {
	geminiReq, err := OpenAIToGemini(req)
	if err != nil {
		return nil, fmt.Errorf("convert to gemini request: %w", err)
	}
	// Always stream upstream so the first token is observable; the ingress
	// layer aggregates when the client asked for a non-streaming response.
	return a.client.DoGenerate(ctx, acc, req.Model, geminiReq, true)
}

func (a *antigravityAdapter) Unwrap(src io.Reader) (io.Reader, error) {
	return antigravity.Unwrap(src)
}

// ---------------------------------------------------------------------------
// Tencent WorkBuddy / CodeBuddy
//
// The upstream is OpenAI-compatible, so the request body is forwarded with only
// the model replaced. Streaming is forced because it is the most stable mode
// upstream; non-streaming clients get the SSE aggregated back into one object.
// ---------------------------------------------------------------------------

type workBuddyAdapter struct {
	client *workbuddy.Client
}

// NewWorkBuddyAdapter wraps the WorkBuddy client as an upstream adapter.
func NewWorkBuddyAdapter(c *workbuddy.Client) Upstream { return &workBuddyAdapter{client: c} }

func (a *workBuddyAdapter) ID() pool.AccountType { return pool.TypeWorkBuddy }
func (a *workBuddyAdapter) Label() string        { return "WorkBuddy" }
func (a *workBuddyAdapter) Format() ResponseFormat {
	return FormatOpenAI
}

func (a *workBuddyAdapter) DefaultModels() []string {
	return a.client.GetDefaultModels()
}

func (a *workBuddyAdapter) FetchModels(ctx context.Context, acc *pool.Account) ([]pool.ModelMetadata, error) {
	return a.client.FetchModels(ctx, acc)
}

func (a *workBuddyAdapter) Chat(ctx context.Context, acc *pool.Account, req *openai.ChatCompletionRequest, rawBody []byte) (*http.Response, error) {
	body, err := withOverrides(rawBody, req, map[string]interface{}{"stream": true})
	if err != nil {
		return nil, err
	}
	return a.client.DoChat(ctx, acc, body)
}

func (a *workBuddyAdapter) Unwrap(src io.Reader) (io.Reader, error) { return src, nil }

// ---------------------------------------------------------------------------
// Qoder (Alibaba)
//
// The inference host exposes an OpenAI-compatible route, so only the model name
// is normalized and the request is decorated with the client metadata block.
// ---------------------------------------------------------------------------

type qoderAdapter struct {
	client *qoder.Client
}

// NewQoderAdapter wraps the Qoder client as an upstream adapter.
func NewQoderAdapter(c *qoder.Client) Upstream { return &qoderAdapter{client: c} }

func (a *qoderAdapter) ID() pool.AccountType { return pool.TypeQoder }
func (a *qoderAdapter) Label() string        { return "Qoder" }
func (a *qoderAdapter) Format() ResponseFormat {
	return FormatOpenAI
}

func (a *qoderAdapter) DefaultModels() []string {
	return a.client.DefaultModelIDs()
}

func (a *qoderAdapter) FetchModels(ctx context.Context, acc *pool.Account) ([]pool.ModelMetadata, error) {
	// The public inference host exposes no model-list route; the client's key
	// set is the catalog.
	return a.client.DefaultModels(), nil
}

func (a *qoderAdapter) Chat(ctx context.Context, acc *pool.Account, req *openai.ChatCompletionRequest, rawBody []byte) (*http.Response, error) {
	req.Model = qoder.NormalizeModel(req.Model)
	body, err := withOverrides(rawBody, req, nil)
	if err != nil {
		return nil, err
	}
	return a.client.Chat(ctx, acc, body)
}

func (a *qoderAdapter) Unwrap(src io.Reader) (io.Reader, error) { return src, nil }

// withOverrides re-encodes a raw request body with the (possibly rewritten)
// model and any adapter-specific field overrides, preserving every other field
// the caller sent.
func withOverrides(rawBody []byte, req *openai.ChatCompletionRequest, overrides map[string]interface{}) ([]byte, error) {
	reqMap := make(map[string]interface{})
	if len(rawBody) > 0 {
		if err := json.Unmarshal(rawBody, &reqMap); err != nil {
			return nil, fmt.Errorf("decode request body: %w", err)
		}
	} else {
		encoded, err := json.Marshal(req)
		if err != nil {
			return nil, fmt.Errorf("encode request body: %w", err)
		}
		if err := json.Unmarshal(encoded, &reqMap); err != nil {
			return nil, fmt.Errorf("decode request body: %w", err)
		}
	}

	if req.Model != "" {
		reqMap["model"] = req.Model
	}
	if req.Messages != nil {
		reqMap["messages"] = req.Messages
	}
	for k, v := range overrides {
		reqMap[k] = v
	}

	return json.Marshal(reqMap)
}
