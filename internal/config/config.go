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
}

type ServerConfig struct {
	Host   string `yaml:"host"`
	Port   int    `yaml:"port"`
	APIKey string `yaml:"api_key"`
	Proxy  string `yaml:"proxy"`
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
	ClientID       string `yaml:"client_id"`
	ClientSecret   string `yaml:"client_secret"`
	Proxy          string `yaml:"proxy"`
}

type WorkBuddyConfig struct {
	DefaultRealm   string `yaml:"default_realm"`   // "cn" or "intl"
	DefaultProduct string `yaml:"default_product"` // "workbuddy", "vscode", "cli"
	// CheckinPath is the daily check-in route appended to the realm's chat
	// host. Empty disables check-in for this provider.
	CheckinPath string `yaml:"checkin_path"`
	// DailyCheckin enables the background daily check-in scheduler.
	DailyCheckin bool `yaml:"daily_checkin"`
	// CheckinHour is the local hour (0-23) the scheduler runs at.
	CheckinHour int `yaml:"checkin_hour"`
}

type RoutingConfig struct {
	DefaultProvider string            `yaml:"default_provider"` // one of the registered provider ids
	ModelRules      []ModelRuleConfig `yaml:"model_rules"`
}

type ModelRuleConfig struct {
	Pattern       string `yaml:"pattern"`
	Provider      string `yaml:"provider"`
	UpstreamModel string `yaml:"upstream_model"`
}

type StorageConfig struct {
	OAuthDirs []string `yaml:"oauth_dirs"`
}

func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Host:   "127.0.0.1",
			Port:   8320,
			APIKey: "sk-allproxyapi",
		},
		Providers: ProvidersConfig{
			Antigravity: AntigravityConfig{
				BaseURL:        "https://daily-cloudcode-pa.googleapis.com",
				DefaultProject: "aicode-consumers",
				// ClientID and ClientSecret have no built-in default: supply the
				// OAuth client your Antigravity install uses in config.yaml.
			},
			WorkBuddy: WorkBuddyConfig{
				DefaultRealm:   "cn",
				DefaultProduct: "workbuddy",
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
				// Qoder aliases (tier + model keys used by the Qoder client).
				{Pattern: "^(qwen|qmodel|qfmodel|kmodel|gmodel|gfmodel|dmodel|dfmodel|mmodel|ultimate|performance|efficient).*", Provider: "qoder", UpstreamModel: ""},
			},
		},
		Storage: StorageConfig{
			OAuthDirs: []string{
				"./data/oauth",
				filepath.Join(os.Getenv("USERPROFILE"), ".cli-proxy-api"),
				filepath.Join(os.Getenv("LOCALAPPDATA"), "CodeBuddyExtension", "Data", "Public", "auth"),
				filepath.Join(os.Getenv("APPDATA"), "Qoder"),
				filepath.Join(os.Getenv("USERPROFILE"), ".qoder"),
			},
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
