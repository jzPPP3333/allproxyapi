package pool

import (
	"sync"
	"time"
)

type AccountType string

const (
	TypeAntigravity AccountType = "antigravity"
	TypeWorkBuddy   AccountType = "workbuddy"
	TypeQoder       AccountType = "qoder"
)

type Account struct {
	mu            sync.RWMutex    `json:"-"`
	ID            string          `json:"id"`
	Type          AccountType     `json:"type"`
	Name          string          `json:"name"`
	FilePath      string          `json:"file_path,omitempty"`
	AccessToken   string          `json:"access_token"`
	RefreshToken  string          `json:"refresh_token"`
	ExpiresAt     time.Time       `json:"expires_at"`
	CooldownUntil time.Time       `json:"cooldown_until"`
	FailStreak    int             `json:"fail_streak"`
	LastError     string          `json:"last_error,omitempty"`
	Models        []ModelMetadata `json:"models,omitempty"`

	// Antigravity specific
	ProjectID string `json:"project_id,omitempty"`
	Email     string `json:"email,omitempty"`

	// WorkBuddy specific
	UID          string `json:"uid,omitempty"`
	Realm        string `json:"realm,omitempty"`   // "cn" or "intl"
	Product      string `json:"product,omitempty"` // "workbuddy", "vscode", "cli"
	EnterpriseID string `json:"enterprise_id,omitempty"`
	Domain       string `json:"domain,omitempty"`

	// Qoder specific
	MachineID    string `json:"machine_id,omitempty"`
	MachineToken string `json:"machine_token,omitempty"`
	AccountName  string `json:"account_name,omitempty"`
}

type ModelMetadata struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	DescriptionZh     string `json:"description_zh,omitempty"`
	Credits           string `json:"credits,omitempty"`
	MaxInputTokens    int    `json:"max_input_tokens,omitempty"`
	MaxOutputTokens   int    `json:"max_output_tokens,omitempty"`
	SupportsImages    bool   `json:"supports_images,omitempty"`
	SupportsReasoning bool   `json:"supports_reasoning,omitempty"`
	SupportsToolCall  bool   `json:"supports_tool_call,omitempty"`
}

func (a *Account) SetModels(models []ModelMetadata) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Models = models
}

func (a *Account) GetModels() []ModelMetadata {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.Models) == 0 {
		return nil
	}
	cp := make([]ModelMetadata, len(a.Models))
	copy(cp, a.Models)
	return cp
}

func (a *Account) IsExpired() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(a.ExpiresAt)
}

func (a *Account) NeedsRefresh(window time.Duration) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().Add(window).After(a.ExpiresAt)
}

func (a *Account) IsCoolingDown() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return time.Now().Before(a.CooldownUntil)
}

func (a *Account) Status() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	now := time.Now()
	if now.Before(a.CooldownUntil) {
		return "cooldown"
	}
	if !a.ExpiresAt.IsZero() && now.After(a.ExpiresAt) {
		return "expired"
	}
	return "healthy"
}

func (a *Account) SetTokens(access, refresh string, expiresAt time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.AccessToken = access
	if refresh != "" {
		a.RefreshToken = refresh
	}
	if !expiresAt.IsZero() {
		a.ExpiresAt = expiresAt
	}
	a.LastError = ""
	a.FailStreak = 0
	a.CooldownUntil = time.Time{}
}

func (a *Account) SetCooldown(dur time.Duration, reason string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.FailStreak++
	if dur <= 0 {
		dur = time.Duration(a.FailStreak*30) * time.Second
		if dur > 10*time.Minute {
			dur = 10 * time.Minute
		}
	}
	a.CooldownUntil = time.Now().Add(dur)
	a.LastError = reason
}

func (a *Account) ResetCooldown() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.CooldownUntil = time.Time{}
	a.FailStreak = 0
	a.LastError = ""
}

func (a *Account) Clone() *Account {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return &Account{
		ID:            a.ID,
		Type:          a.Type,
		Name:          a.Name,
		FilePath:      a.FilePath,
		AccessToken:   a.AccessToken,
		RefreshToken:  a.RefreshToken,
		ExpiresAt:     a.ExpiresAt,
		CooldownUntil: a.CooldownUntil,
		FailStreak:    a.FailStreak,
		LastError:     a.LastError,
		ProjectID:     a.ProjectID,
		Email:         a.Email,
		UID:           a.UID,
		Realm:         a.Realm,
		Product:       a.Product,
		EnterpriseID:  a.EnterpriseID,
		Domain:        a.Domain,
		MachineID:     a.MachineID,
		MachineToken:  a.MachineToken,
		AccountName:   a.AccountName,
		Models:        a.GetModels(),
	}
}
