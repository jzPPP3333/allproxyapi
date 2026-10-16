package qoder

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jzPPP3333/allproxyapi/internal/config"
	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

// QuotaCooldown is how long an account is parked after the upstream reports
// exhausted quota. Consumption refills on a slow cadence, so a short
// rate-limit window would only retry the same account into the same wall.
const QuotaCooldown = 30 * time.Minute

// quotaCodes are the application-level codes Qoder returns inside a 200 or 4xx
// body when the account has no remaining allowance.
var quotaCodes = map[int]bool{
	6004:  true, // insufficient credits
	6005:  true, // daily quota reached
	10006: true, // plan limit reached
	42901: true, // concurrency quota
}

// isQuotaExhausted reports whether a status code identifies exhausted quota
// rather than a generic failure. 402 is Payment Required and always counts.
func isQuotaExhausted(status int) bool { return status == http.StatusPaymentRequired }

// quotaCodeOf extracts an application-level code from a response body so the
// cooldown reason can name it. It returns 0 when no known code is present.
func quotaCodeOf(raw []byte) int {
	if len(raw) == 0 {
		return 0
	}
	var probe struct {
		Code int `json:"code"`
		Data *struct {
			Code int `json:"code"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return 0
	}
	for _, candidate := range []int{probe.Code, codeOf(probe.Data)} {
		if quotaCodes[candidate] {
			return candidate
		}
	}
	return 0
}

func codeOf(d *struct {
	Code int `json:"code"`
}) int {
	if d == nil {
		return 0
	}
	return d.Code
}

// Client talks to one Qoder region.
type Client struct {
	cfg        config.QoderConfig
	httpClient *http.Client
	region     string
	cosy       *Config // nil when no public key is configured
}

func NewClient(cfg config.QoderConfig) *Client {
	return &Client{
		cfg:        cfg,
		httpClient: newHTTPClient(cfg.Proxy, 300*time.Second),
		region:     regionOf(cfg.Region),
		cosy:       buildCOSYConfig(cfg.COSY),
	}
}

// buildCOSYConfig mirrors the YAML block into the crypto Config, parsing any
// PEM material. A key that fails to parse is reported and the envelope stays
// off rather than silently sending requests the upstream cannot read.
func buildCOSYConfig(c config.COSYConfig) *Config {
	if c.PublicKeyPEM == "" {
		return nil
	}
	pub, err := ParsePublicKeyPEM([]byte(c.PublicKeyPEM))
	if err != nil {
		log.Printf("[qoder] providers.qoder.cosy.public_key_pem is invalid, COSY disabled: %v", err)
		return nil
	}
	out := &Config{
		PublicKey: pub,
		KeyAlg:    c.KeyAlg,
		Alphabet:  MustAlphabet(),
	}
	if c.Alphabet != "" {
		alpha, albErr := NewAlphabet(c.Alphabet)
		if albErr != nil {
			log.Printf("[qoder] providers.qoder.cosy.alphabet is invalid, using default: %v", albErr)
		} else {
			out.Alphabet = alpha
		}
	}
	if c.PrivateKeyPEM != "" {
		if priv, pkErr := ParsePrivateKeyPEM([]byte(c.PrivateKeyPEM)); pkErr == nil {
			out.PrivateKey = priv
		} else {
			log.Printf("[qoder] providers.qoder.cosy.private_key_pem is invalid, response decryption unavailable: %v", pkErr)
		}
	}
	if c.SignKey != "" {
		out.SignKey = []byte(c.SignKey)
	}
	return out
}

// uuid produces a random RFC 4122 v4 identifier for the request/session headers.
func uuid() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// metadataBlock carries the client context the inference host expects.
type metadataBlock struct {
	Context struct {
		RequestID    string `json:"request_id"`
		RequestSetID string `json:"request_set_id"`
		SessionID    string `json:"session_id"`
		TaskID       string `json:"task_id"`
		ClientType   string `json:"client_type"`
	} `json:"context"`
}

// Chat forwards an OpenAI-shaped request to the Qoder inference host.
func (c *Client) Chat(ctx context.Context, acc *pool.Account, reqBody []byte) (*http.Response, error) {
	endpoint := resolveBaseURL(c.regionOf(acc), c.cfg.BaseURL) + ChatPath

	body, err := c.decorate(reqBody)
	if err != nil {
		return nil, err
	}

	// The fingerprint is derived, not random: it must stay constant across
	// restarts or the upstream sees a new device on every launch.
	EnsureFingerprint(acc)

	reqID := uuid()
	sessionID := uuid()

	// When the COSY key is configured, the request travels as an encrypted
	// envelope rather than a plain body.
	var payload []byte
	enveloped := c.cosy.Enabled()
	if enveloped {
		sealed, sealErr := Seal(body, c.cosy)
		if sealErr != nil {
			return nil, wrapErr("seal request", sealErr)
		}
		payload = []byte(sealed)
	} else {
		payload = body
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, wrapErr("build chat request", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+acc.AccessToken)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream, application/json")
	httpReq.Header.Set("X-Request-ID", reqID)
	httpReq.Header.Set("X-Session-ID", sessionID)
	httpReq.Header.Set("X-Client-Version", c.clientVersion())
	if enveloped {
		httpReq.Header.Set("X-Cosy-Alg", CipherAlg)
		httpReq.Header.Set("X-Cosy-Version", strconv.Itoa(EnvelopeVersion))
	}
	if acc.MachineID != "" {
		httpReq.Header.Set("X-Machine-Id", acc.MachineID)
	}
	if acc.MachineToken != "" {
		httpReq.Header.Set("X-Machine-Token", acc.MachineToken)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, wrapErr("chat request", err)
	}

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		resp.Body.Close()
		acc.SetCooldown(60*time.Second, "429 Too Many Requests")
		return nil, fmt.Errorf("qoder rate limit (429)")

	case isQuotaExhausted(resp.StatusCode):
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		reason := fmt.Sprintf("%d quota exhausted", resp.StatusCode)
		if code := quotaCodeOf(raw); code != 0 {
			reason = fmt.Sprintf("%d quota exhausted (code %d)", resp.StatusCode, code)
		}
		// Quota exhaustion is not transient: cooling the account down for the
		// rate-limit window would just burn the same failing account again.
		acc.SetCooldown(QuotaCooldown, reason)
		return nil, fmt.Errorf("qoder %s: %s", reason, truncate(raw, 256))

	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		resp.Body.Close()
		if refreshErr := RefreshAccount(ctx, acc, c.regionOf(acc), c.cfg.AuthURL, c.cfg.Proxy); refreshErr == nil {
			return c.Chat(ctx, acc, reqBody)
		}
		acc.SetCooldown(300*time.Second, fmt.Sprintf("%d Unauthorized", resp.StatusCode))
		return nil, fmt.Errorf("qoder unauthorized (%d)", resp.StatusCode)

	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("qoder upstream error (%d): %s", resp.StatusCode, truncate(raw, 512))
	}

	return resp, nil
}

// decorate injects the metadata block the inference host expects while leaving
// the caller's OpenAI fields intact.
func (c *Client) decorate(reqBody []byte) ([]byte, error) {
	var reqMap map[string]interface{}
	if err := json.Unmarshal(reqBody, &reqMap); err != nil {
		return nil, wrapErr("decode request body", err)
	}
	if _, ok := reqMap["stream_options"]; !ok {
		reqMap["stream_options"] = map[string]interface{}{"include_usage": true}
	}

	var meta metadataBlock
	meta.Context.RequestID = uuid()
	meta.Context.RequestSetID = uuid()
	meta.Context.SessionID = uuid()
	meta.Context.TaskID = "common"
	meta.Context.ClientType = "qodercli"
	reqMap["metadata"] = meta

	return json.Marshal(reqMap)
}

func (c *Client) clientVersion() string {
	if c.cfg.ClientVersion != "" {
		return c.cfg.ClientVersion
	}
	return "1.1.49"
}

func (c *Client) regionOf(acc *pool.Account) string {
	if acc != nil && acc.Realm != "" {
		return regionOf(acc.Realm)
	}
	return c.region
}

// DefaultModels returns the static catalog of routable Qoder model keys.
func (c *Client) DefaultModels() []pool.ModelMetadata {
	return []pool.ModelMetadata{
		{ID: "ultimate", Name: "Ultimate", DescriptionZh: "Qoder 最高能力档位，自动路由至旗舰模型", SupportsToolCall: true, SupportsReasoning: true},
		{ID: "performance", Name: "Performance", DescriptionZh: "Qoder 均衡档位，性能与延迟平衡", SupportsToolCall: true},
		{ID: "efficient", Name: "Efficient", DescriptionZh: "Qoder 极速档位，低延迟轻量任务", SupportsToolCall: true},
		{ID: "qmodel_38max", Name: "Qwen3.8-Max", DescriptionZh: "通义千问旗舰模型", SupportsToolCall: true, SupportsReasoning: true},
		{ID: "qmodel_latest", Name: "Qwen3.7-Max", DescriptionZh: "通义千问 3.7 旗舰", SupportsToolCall: true, SupportsReasoning: true},
		{ID: "qmodel", Name: "Qwen3.7-Plus", DescriptionZh: "通义千问 3.7 Plus", SupportsToolCall: true},
		{ID: "qfmodel", Name: "Qwen3.8-Flash", DescriptionZh: "通义千问 3.8 极速版", SupportsToolCall: true},
		{ID: "kmodel_latest", Name: "Kimi-K3", DescriptionZh: "月之暗面 Kimi K3", SupportsToolCall: true, SupportsReasoning: true},
		{ID: "kmodel", Name: "Kimi-K2.7-Code", DescriptionZh: "Kimi 代码专用模型", SupportsToolCall: true},
		{ID: "gmodel", Name: "GLM-5.3", DescriptionZh: "智谱 GLM-5.3", SupportsToolCall: true, SupportsReasoning: true},
		{ID: "gfmodel", Name: "GLM-5.3-Flash", DescriptionZh: "智谱 GLM-5.3 极速版", SupportsToolCall: true},
		{ID: "dmodel", Name: "DeepSeek-V4-Pro", DescriptionZh: "DeepSeek V4 Pro", SupportsToolCall: true, SupportsReasoning: true},
		{ID: "dfmodel", Name: "DeepSeek-V4-Flash", DescriptionZh: "DeepSeek V4 极速版", SupportsToolCall: true},
		{ID: "mmodel", Name: "MiniMax-M3", DescriptionZh: "MiniMax M3", SupportsToolCall: true},
	}
}

// DefaultModelIDs is the string form of DefaultModels, used by /v1/models.
func (c *Client) DefaultModelIDs() []string {
	models := c.DefaultModels()
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	return ids
}

// NormalizeModel maps a public alias onto a Qoder model key. Unknown names pass
// through unchanged so callers can address keys directly.
func NormalizeModel(model string) string {
	lower := strings.ToLower(strings.TrimSpace(model))
	if lower == "" {
		return "ultimate"
	}
	if _, ok := knownKeys[lower]; ok {
		return lower
	}

	switch {
	case strings.Contains(lower, "minimax") || strings.HasPrefix(lower, "mmodel"):
		return "mmodel"
	case strings.Contains(lower, "deepseek"):
		if strings.Contains(lower, "flash") {
			return "dfmodel"
		}
		return "dmodel"
	case strings.Contains(lower, "kimi"):
		if strings.Contains(lower, "code") || strings.Contains(lower, "k2") {
			return "kmodel"
		}
		return "kmodel_latest"
	case strings.Contains(lower, "glm"):
		if strings.Contains(lower, "flash") {
			return "gfmodel"
		}
		return "gmodel"
	case strings.Contains(lower, "qwen"):
		switch {
		case strings.Contains(lower, "max"):
			return "qmodel_38max"
		case strings.Contains(lower, "flash"):
			return "qfmodel"
		default:
			return "qmodel"
		}
	}
	return model
}

var knownKeys = map[string]struct{}{
	"ultimate": {}, "performance": {}, "efficient": {},
	"qmodel_38max": {}, "qmodel_latest": {}, "qmodel": {}, "qfmodel": {},
	"kmodel_latest": {}, "kmodel": {},
	"gmodel": {}, "gfmodel": {},
	"dmodel": {}, "dfmodel": {},
	"mmodel": {},
}
