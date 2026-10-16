package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Providers ProvidersConfig `yaml:"providers"`
	Routing   RoutingConfig   `yaml:"routing"`
	Storage   StorageConfig   `yaml:"storage"`
	Pool      PoolConfig      `yaml:"pool"`
}

type ServerConfig struct {
	Host   string `yaml:"host"`
	Port   int    `yaml:"port"`
	APIKey string `yaml:"api_key"`
	Proxy  string `yaml:"proxy"`
	// LogLevel filters the request log: "debug", "info", "warn" or "error".
	// Empty means info. Debug also logs every upstream request line.
	LogLevel string `yaml:"log_level"`
}

// PoolConfig tunes the account pool's cooldown behaviour.
type PoolConfig struct {
	// SoftCooldownBaseMinutes is the first soft cooldown after a rate limit.
	// Each consecutive failure doubles it, up to SoftCooldownMaxMinutes.
	// Zero uses the built-in defaults.
	SoftCooldownBaseMinutes int `yaml:"soft_cooldown_base_minutes"`
	// SoftCooldownMaxMinutes caps the exponential backoff.
	SoftCooldownMaxMinutes int `yaml:"soft_cooldown_max_minutes"`
	// KeepaliveHours is how often to refresh each account's credit snapshot
	// with a lightweight read. Zero disables the keepalive.
	KeepaliveHours int `yaml:"keepalive_hours"`
	// CreditRefreshHours is how often to re-read remaining allowance.
	CreditRefreshHours int `yaml:"credit_refresh_hours"`
}

type ProvidersConfig struct {
	Antigravity AntigravityConfig `yaml:"antigravity"`
	WorkBuddy   WorkBuddyConfig   `yaml:"workbuddy"`
	Qoder       QoderConfig       `yaml:"qoder"`
}

type QoderConfig struct {
	// Region selects the default host pair: "intl" or "cn".
	Region string `yaml:"region"`
	// BaseURL is the inference host serving the OpenAI-compatible
	// /model/v1/chat/completions endpoint. Leave empty to derive from Region.
	BaseURL string `yaml:"base_url"`
	// AuthURL serves PAT exchange and token refresh. Leave empty to derive from Region.
	AuthURL string `yaml:"auth_url"`
	// ClientVersion is the client build reported in the request metadata.
	ClientVersion string `yaml:"client_version"`
	// UserAgent overrides the identifier sent upstream. Empty uses the default
	// Go client string, matching the observed request shape.
	UserAgent string `yaml:"user_agent"`
	// Proxy is an optional per-provider egress proxy.
	Proxy string `yaml:"proxy"`
	// COSY configures the encrypted request envelope. Leaving the public key
	// empty sends requests unencrypted, which the public inference host accepts.
	COSY COSYConfig `yaml:"cosy"`
	// CheckinPath is the daily check-in route appended to the auth host. Empty
	// disables check-in for this provider.
	CheckinPath string `yaml:"checkin_path"`
	// DailyCheckin enables the background daily check-in scheduler.
	DailyCheckin bool `yaml:"daily_checkin"`
	// CheckinHour is the local hour (0-23) the scheduler runs at.
	CheckinHour int `yaml:"checkin_hour"`
}

// COSYConfig carries the COSY envelope key material. Key values are PEM blocks
// copied from your own Qoder client; they are not shipped with this repo, the
// same way the Antigravity OAuth client credentials are config-only.
type COSYConfig struct {
	// PublicKeyPEM is the server RSA public key that wraps the session key.
	PublicKeyPEM string `yaml:"public_key_pem"`
	// PrivateKeyPEM is only needed to decrypt a response that echoes the envelope.
	PrivateKeyPEM string `yaml:"private_key_pem"`
	// KeyAlg selects the RSA padding: "RSA-OAEP-SHA256" (default) or "RSA-PKCS1v15".
	KeyAlg string `yaml:"key_alg"`
	// Alphabet overrides the 64-character base64 dictionary.
	Alphabet string `yaml:"alphabet"`
	// SignKey enables the envelope HMAC when non-empty.
	SignKey string `yaml:"sign_key"`
}

type AntigravityConfig struct {
	BaseURL        string `yaml:"base_url"`
	DefaultProject string `yaml:"default_project"`
	// UserAgent overrides the identifier sent upstream. Empty uses the client
	// build this gateway emulates, which is what the upstream expects.
	UserAgent    string `yaml:"user_agent"`
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	Proxy        string `yaml:"proxy"`
}

type WorkBuddyConfig struct {
	DefaultRealm   string `yaml:"default_realm"`   // "cn" or "intl"
	DefaultProduct string `yaml:"default_product"` // "workbuddy", "vscode", "cli"
	// UserAgent overrides the identifier sent upstream. Empty uses the per-realm
	// WorkBuddy build string.
	UserAgent string `yaml:"user_agent"`
	// CheckinPath is the daily check-in route appended to the realm's chat
	// host. Empty disables check-in for this provider.
	CheckinPath string `yaml:"checkin_path"`
	// DailyCheckin enables the background daily check-in scheduler.
	DailyCheckin bool `yaml:"daily_checkin"`
	// CheckinHour is the local hour (0-23) the scheduler runs at.
	CheckinHour int `yaml:"checkin_hour"`
	// ExcludeModelPatterns filters the upstream model list by regexp, matched
	// against the model id. The upstream exposes tier entries such as
	// "default-model" and "primary-model" alongside real models; those are
	// server-side routing presets rather than selectable models, so they are
	// excluded by default. Patterns are config, not code, so an upstream
	// rename needs no rebuild.
	ExcludeModelPatterns []string `yaml:"exclude_model_patterns"`
}

// DefaultExcludedModelPatterns drops the upstream's tier presets from the
// model list.
//
// These ids come from an observed upstream response, not from documentation:
// the list mixes presets such as "auto" and "default" in with concrete models
// like "glm-5.3". A preset is a server-side alias that resolves to a real model
// at request time, so listing it as a model would misrepresent what is
// selectable.
//
// The names are configuration precisely because they are upstream-defined and
// have changed before; if a preset appears that is not matched here, add it to
// exclude_model_patterns rather than editing this list.
var DefaultExcludedModelPatterns = []string{
	`(?i)^auto$`,
	`(?i)^default$`,
	`(?i)^lite$`,
}

type RoutingConfig struct {
	DefaultProvider string            `yaml:"default_provider"` // one of the registered provider ids
	ModelRules      []ModelRuleConfig `yaml:"model_rules"`
	// ModelMapping is an explicit alias table, consulted before the regex
	// rules. Keys are matched case-insensitively against the requested name.
	// A value is either "<provider>/<model>" to pin both, or a bare model name
	// to override only the upstream model while the rules pick the provider.
	//
	// This exists for the cases the rules cannot express: a client that only
	// speaks standard names ("claude-3-5-sonnet", "qwen-max") pointed at an
	// upstream that renamed its models.
	ModelMapping map[string]string `yaml:"model_mapping"`
}

type ModelRuleConfig struct {
	Pattern       string `yaml:"pattern"`
	Provider      string `yaml:"provider"`
	UpstreamModel string `yaml:"upstream_model"`
}

type StorageConfig struct {
	OAuthDirs []string `yaml:"oauth_dirs"`
}

// qoderSearchDirs lists the credential locations scanned at startup.
//
// Qoder ships as two products and the CN build keeps its state under
// ~/.qoder-cn rather than ~/.qoder, so both names are listed for both
// products. The client also honours QODER_CONFIG_DIR and QODERCN_CONFIG_DIR,
// which are read here so a relocated install is still found.
func qoderSearchDirs() []string {
	home := os.Getenv("USERPROFILE")
	if home == "" {
		home, _ = os.UserHomeDir()
	}

	dirs := []string{
		"./data/oauth",
		filepath.Join(home, ".cli-proxy-api"),

		// WorkBuddy / CodeBuddy desktop credentials.
		filepath.Join(os.Getenv("LOCALAPPDATA"), "CodeBuddyExtension", "Data", "Public", "auth"),

		// Qoder installs, plus the CLI config roots the client honours.
		filepath.Join(os.Getenv("APPDATA"), "Qoder"),
		filepath.Join(os.Getenv("APPDATA"), "Qoder CN"),
		filepath.Join(home, ".qoder"),
		filepath.Join(home, ".qoder-cn"),
		os.Getenv("QODER_CONFIG_DIR"),
		os.Getenv("QODERCN_CONFIG_DIR"),
	}

	// The client keeps auth material in an .auth subdirectory, and the scan is
	// one level deep, so those are listed explicitly.
	for _, root := range append([]string{home}, os.Getenv("QODER_CONFIG_DIR"), os.Getenv("QODERCN_CONFIG_DIR")) {
		if strings.TrimSpace(root) == "" {
			continue
		}
		dirs = append(dirs,
			filepath.Join(root, ".qoder", ".auth"),
			filepath.Join(root, ".qoder-cn", ".auth"),
		)
	}

	// Drop empties so a missing environment variable does not add a bare path.
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if strings.TrimSpace(d) != "" {
			out = append(out, d)
		}
	}
	return out
}

func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Host:     "127.0.0.1",
			Port:     8320,
			APIKey:   "sk-allproxyapi",
			LogLevel: "info",
		},
		Pool: PoolConfig{
			// These mirror pool.SoftCooldownBase / SoftCooldownMax. They are
			// stated here rather than imported because the pool package depends
			// on config, so referencing it would be a cycle.
			SoftCooldownBaseMinutes: 10,
			SoftCooldownMaxMinutes:  120,
			KeepaliveHours:          6,
			CreditRefreshHours:      3,
		},
		Providers: ProvidersConfig{
			Antigravity: AntigravityConfig{
				BaseURL:        "https://daily-cloudcode-pa.googleapis.com",
				DefaultProject: "aicode-consumers",
				// ClientID and ClientSecret have no built-in default: supply the
				// OAuth client your Antigravity install uses in config.yaml.
			},
			WorkBuddy: WorkBuddyConfig{
				DefaultRealm:         "cn",
				DefaultProduct:       "workbuddy",
				ExcludeModelPatterns: DefaultExcludedModelPatterns,
			},
			Qoder: QoderConfig{
				Region:        "intl",
				ClientVersion: "1.1.49",
			},
		},
		Routing: RoutingConfig{
			DefaultProvider: "workbuddy",
			ModelRules: []ModelRuleConfig{
				// upstream_model is deliberately empty. The adapter's own
				// normalizer maps friendly aliases; forcing one name here
				// collapsed every claude-* model onto a single upstream model.
				{Pattern: "^claude-.*", Provider: "antigravity", UpstreamModel: ""},
				{Pattern: "^gemini-.*", Provider: "antigravity", UpstreamModel: ""},
				{Pattern: "^deepseek-.*", Provider: "workbuddy", UpstreamModel: ""},
				{Pattern: "^kimi-.*", Provider: "workbuddy", UpstreamModel: ""},
				{Pattern: "^hy.*", Provider: "workbuddy", UpstreamModel: ""},
				{Pattern: "^glm-.*", Provider: "workbuddy", UpstreamModel: ""},
				{Pattern: "^gpt-.*", Provider: "workbuddy", UpstreamModel: ""},
				// Qoder's tier presets. Matched as exact names because "auto"
				// and "lite" are also WorkBuddy tier ids; use "qoder/auto" to
				// address Qoder's unambiguously.
				{Pattern: "^(ultimate|performance|efficient)$", Provider: "qoder", UpstreamModel: ""},
				// Named Qoder models, if the upstream starts serving them.
				{Pattern: "^(qwen|qmodel|qfmodel|kmodel|gmodel|gfmodel|dmodel|dfmodel|mmodel).*", Provider: "qoder", UpstreamModel: ""},
			},
		},
		Storage: StorageConfig{
			OAuthDirs: qoderSearchDirs(),
		},
	}
}

func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()

	if _, err := os.Stat(path); os.IsNotExist(err) {
		// Save default config if not exists
		if err := SaveConfig(path, cfg); err != nil {
			return nil, fmt.Errorf("failed to save default config: %w", err)
		}
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	// Expand environment variables in OAuthDirs
	for i, dir := range cfg.Storage.OAuthDirs {
		if strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, "~\\") {
			home, _ := os.UserHomeDir()
			cfg.Storage.OAuthDirs[i] = filepath.Join(home, dir[2:])
		}
	}

	// Alias keys are matched case-insensitively, so they are folded once here
	// rather than on every request.
	if len(cfg.Routing.ModelMapping) > 0 {
		folded := make(map[string]string, len(cfg.Routing.ModelMapping))
		for alias, target := range cfg.Routing.ModelMapping {
			folded[strings.ToLower(strings.TrimSpace(alias))] = target
		}
		cfg.Routing.ModelMapping = folded
	}

	return cfg, nil
}

func SaveConfig(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
