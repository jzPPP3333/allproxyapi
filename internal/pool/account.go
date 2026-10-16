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

	// Lifecycle state. hardCooldownUntil is an exhausted-allowance freeze that
	// outlasts any soft cooldown; disabled means the credential cannot be
	// repaired automatically.
	hardCooldownUntil time.Time            `json:"-"`
	disabled          bool                 `json:"-"`
	modelCooldowns    map[string]time.Time `json:"-"`

	// CreditExpiresAt is when the account's remaining free allowance lapses.
	// Dispatch prefers accounts whose credit is about to expire, since that
	// allowance is lost rather than rolled over.
	CreditExpiresAt time.Time `json:"credit_expires_at,omitempty"`
	// Credits is the last observed remaining allowance, for display and for
	// prioritising dispatch. It is informational: dispatch never depends on it
	// being accurate.
	Credits string `json:"credits,omitempty"`

	// InFlight is the number of requests currently dispatched to this account,
	// used to avoid overloading one account under concurrency.
	InFlight int `json:"in_flight,omitempty"`

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

// Status is the display string for the account's availability. It delegates to
// the lifecycle state so there is one source of truth; the legacy "healthy",
// "cooldown" and "expired" names are preserved for existing consumers.
func (a *Account) Status() string {
	switch a.State() {
	case StateDisabled:
		return "dead"
	case StateHardCooldown:
		return "frozen"
	case StateSoftCooldown:
		return "cooling"
	}
	// An active account whose token has lapsed is still usable: the dispatcher
	// lets it through and the credential is refreshed on demand.
	if a.IsExpired() {
		return "expired"
	}
	return "healthy"
}

// SetTokens installs fresh credentials.
//
// A successful refresh proves the credential is good, so it also clears a soft
// cooldown and re-enables a disabled account: the reason the account was out of
// rotation no longer applies. A hard cooldown is left alone, because a valid
// token does not restore an exhausted allowance.
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
	a.disabled = false
}

// SetCooldown parks the account for a soft cooldown. It is kept for callers
// that only know a duration; the state-specific setters express intent better.
func (a *Account) SetCooldown(dur time.Duration, reason string) {
	a.SetSoftCooldown(dur, reason)
}

// ResetCooldown clears every cooldown on the account, for a manual operator
// reset.
func (a *Account) ResetCooldown() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.CooldownUntil = time.Time{}
	a.hardCooldownUntil = time.Time{}
	a.modelCooldowns = nil
	a.FailStreak = 0
	a.LastError = ""
	a.disabled = false
}

func (a *Account) Clone() *Account {
	a.mu.RLock()
	defer a.mu.RUnlock()
	c := &Account{
		ID:                a.ID,
		Type:              a.Type,
		Name:              a.Name,
		FilePath:          a.FilePath,
		AccessToken:       a.AccessToken,
		RefreshToken:      a.RefreshToken,
		ExpiresAt:         a.ExpiresAt,
		CooldownUntil:     a.CooldownUntil,
		FailStreak:        a.FailStreak,
		LastError:         a.LastError,
		ProjectID:         a.ProjectID,
		Email:             a.Email,
		UID:               a.UID,
		Realm:             a.Realm,
		Product:           a.Product,
		EnterpriseID:      a.EnterpriseID,
		Domain:            a.Domain,
		MachineID:         a.MachineID,
		MachineToken:      a.MachineToken,
		AccountName:       a.AccountName,
		hardCooldownUntil: a.hardCooldownUntil,
		disabled:          a.disabled,
		CreditExpiresAt:   a.CreditExpiresAt,
		Credits:           a.Credits,
		InFlight:          a.InFlight,
		Models:            a.GetModels(),
	}
	if len(a.modelCooldowns) > 0 {
		c.modelCooldowns = make(map[string]time.Time, len(a.modelCooldowns))
		for m, until := range a.modelCooldowns {
			c.modelCooldowns[m] = until
		}
	}
	return c
}
