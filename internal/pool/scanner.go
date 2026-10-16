package pool

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrEncryptedCredential reports a credential file whose tokens are sealed in a
// WorkBuddy encrypted envelope. The key is held in the client's OS-protected
// store, so the value must be exported from the running client.
var ErrEncryptedCredential = errors.New("credential is encrypted")

type GenericOAuthFile struct {
	Type         string `json:"type"` // "antigravity", "workbuddy", etc.
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Expired      string `json:"expired"` // RFC3339 string
	Timestamp    int64  `json:"timestamp"`

	// WorkBuddy desktop state files nest tokens under "auth" and identity
	// under "account" rather than at the top level.
	Auth    *DesktopAuth    `json:"auth"`
	Account *DesktopAccount `json:"account"`

	// Antigravity
	ProjectID string `json:"project_id"`
	Email     string `json:"email"`

	// WorkBuddy
	UID          string `json:"uid"`
	Realm        string `json:"realm"`
	Product      string `json:"product"`
	Domain       string `json:"domain"`
	EnterpriseID string `json:"enterprise_id"`

	// WorkBuddy Desktop format variants (camelCase)
	AccessTokenCamel  string `json:"accessToken"`
	RefreshTokenCamel string `json:"refreshToken"`
	UserIDCamel       string `json:"userId"`
	UIDCamel          string `json:"uidCamel"`
	TenantIDCamel     string `json:"tenantId"`

	// Qoder
	MachineID    string `json:"machine_id"`
	MachineToken string `json:"machine_token"`
	AccountName  string `json:"account_name"`

	// Nested token
	Token *struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Expiry       string `json:"expiry"`
	} `json:"token"`

	// Nested data
	Data *struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
	} `json:"data"`

	// Capabilities & models
	AntigravityCapabilityHints *struct {
		Endpoint string   `json:"endpoint"`
		ModelIDs []string `json:"model_ids"`
	} `json:"antigravity_capability_hints"`
	Models []string `json:"models"`
}

// DesktopAuth is the token block of a WorkBuddy desktop state file. The token
// values are strings in the older files; newer ones wrap them in an encrypted
// envelope object, which unmarshalling into a string rejects, so both shapes
// are captured.
type DesktopAuth struct {
	AccessToken  json.RawMessage `json:"accessToken"`
	RefreshToken json.RawMessage `json:"refreshToken"`
	ExpiresAt    int64           `json:"expiresAt"`
	Domain       string          `json:"domain"`
}

// DesktopAccount is the identity block of a WorkBuddy desktop state file.
// Every free-text field can be a plain string or an encrypted envelope, so all
// of them are raw: typing one as string would make the whole file fail to
// unmarshal before the encryption could be reported.
type DesktopAccount struct {
	UID      string          `json:"uid"`
	Nickname json.RawMessage `json:"nickname"`
	Phone    json.RawMessage `json:"phoneNumber"`
}

// asString returns the raw value when it is a plain JSON string. An encrypted
// envelope is an object and yields "", which the caller reports rather than
// treating as absent.
func asString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// isEncryptedEnvelope reports whether a raw value is a WorkBuddy encrypted
// envelope rather than a plain string.
func isEncryptedEnvelope(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	return !strings.HasPrefix(strings.TrimSpace(string(raw)), `"`)
}

func ScanDirectories(pool *AccountPool, dirs []string) int {
	totalLoaded := 0
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		loaded := ScanDirectory(pool, dir)
		totalLoaded += loaded
	}
	return totalLoaded
}

func ScanDirectory(pool *AccountPool, dir string) int {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return 0
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("[Scanner] failed to read dir %s: %v", dir, err)
		return 0
	}

	loaded := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		nameLower := strings.ToLower(entry.Name())
		if strings.Contains(nameLower, ".sample.") || strings.HasPrefix(nameLower, "sample") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".json" && ext != ".info" {
			continue
		}

		fullPath := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		acc, err := ParseAccountData(fullPath, data)
		if err != nil {
			// An encrypted credential is reported rather than skipped, so the
			// user learns why the file did not load instead of seeing an empty
			// pool with no explanation.
			if errors.Is(err, ErrEncryptedCredential) {
				log.Printf("[Scanner] 跳过 %s: %v", entry.Name(), err)
			}
			continue
		}
		if acc != nil {
			pool.AddOrUpdate(acc)
			loaded++
			log.Printf("[Scanner] Loaded %s account '%s' from %s", acc.Type, acc.Name, entry.Name())
		}
	}
	return loaded
}

func ParseAccountData(filePath string, data []byte) (*Account, error) {
	var raw GenericOAuthFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	baseName := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))

	// Resolve tokens from multiple schema variations. The WorkBuddy desktop
	// state files nest them under "auth"; older exports put them at the top
	// level, so every shape is probed before giving up.
	var desktopAuth *DesktopAuth
	if raw.Auth != nil && (len(raw.Auth.AccessToken) > 0 || len(raw.Auth.RefreshToken) > 0) {
		desktopAuth = raw.Auth
	}

	accessToken := raw.AccessToken
	if accessToken == "" {
		accessToken = raw.AccessTokenCamel
	}
	if accessToken == "" && raw.Token != nil {
		accessToken = raw.Token.AccessToken
	}
	if accessToken == "" && raw.Data != nil {
		accessToken = raw.Data.AccessToken
	}
	if accessToken == "" && desktopAuth != nil {
		accessToken = asString(desktopAuth.AccessToken)
	}

	refreshToken := raw.RefreshToken
	if refreshToken == "" {
		refreshToken = raw.RefreshTokenCamel
	}
	if refreshToken == "" && raw.Token != nil {
		refreshToken = raw.Token.RefreshToken
	}
	if refreshToken == "" && raw.Data != nil {
		refreshToken = raw.Data.RefreshToken
	}
	if refreshToken == "" && desktopAuth != nil {
		refreshToken = asString(desktopAuth.RefreshToken)
	}

	if accessToken == "" && refreshToken == "" {
		// Distinguish "the file holds an encrypted credential the gateway
		// cannot read" from "the file holds no credential", so the reason is
		// visible instead of the file silently not loading.
		if desktopAuth != nil && (isEncryptedEnvelope(desktopAuth.AccessToken) || isEncryptedEnvelope(desktopAuth.RefreshToken)) {
			return nil, fmt.Errorf("%w (token is $wbEncrypted; see README 凭据配置 for import)", ErrEncryptedCredential)
		}
		return nil, fmt.Errorf("no tokens found in file")
	}

	// Resolve expiration
	var expiresAt time.Time
	if raw.Expired != "" {
		if t, err := time.Parse(time.RFC3339, raw.Expired); err == nil {
			expiresAt = t
		}
	} else if raw.ExpiresIn > 0 && raw.Timestamp > 0 {
		expiresAt = time.UnixMilli(raw.Timestamp).Add(time.Duration(raw.ExpiresIn) * time.Second)
	} else if raw.Token != nil && raw.Token.Expiry != "" {
		if t, err := time.Parse(time.RFC3339, raw.Token.Expiry); err == nil {
			expiresAt = t
		}
	} else if desktopAuth != nil && desktopAuth.ExpiresAt > 0 {
		// The desktop files carry an absolute epoch-millisecond expiry.
		expiresAt = time.UnixMilli(desktopAuth.ExpiresAt)
	}

	// Determine Account Type via heuristics
	pathLower := strings.ToLower(filePath)
	typeLower := strings.ToLower(raw.Type)

	// Identity and region come from either the top level or the desktop
	// state file's nested blocks.
	uid := raw.UID
	if uid == "" {
		uid = raw.UserIDCamel
	}
	if uid == "" {
		uid = raw.UIDCamel
	}
	if uid == "" && raw.Account != nil {
		uid = raw.Account.UID
	}
	domain := raw.Domain
	if domain == "" && desktopAuth != nil {
		domain = desktopAuth.Domain
	}

	// A desktop state file is a WorkBuddy artifact even when its tokens are
	// sealed, so its presence identifies the provider.
	isDesktopState := raw.Account != nil || desktopAuth != nil

	isAntigravity := typeLower == "antigravity" || strings.Contains(pathLower, "antigravity") || raw.ProjectID != ""
	isQoder := !isAntigravity && (typeLower == "qoder" ||
		strings.Contains(pathLower, "qoder") ||
		isQoderToken(accessToken) || isQoderToken(refreshToken))
	isWorkBuddy := !isAntigravity && !isQoder &&
		(typeLower == "workbuddy" || strings.Contains(pathLower, "workbuddy") ||
			strings.Contains(pathLower, "codebuddy") || uid != "" || isDesktopState)

	// Fallback heuristic: check token format or fields
	if !isAntigravity && !isWorkBuddy && !isQoder {
		if strings.HasPrefix(accessToken, "ya29.") || strings.HasPrefix(refreshToken, "1//") {
			isAntigravity = true
		} else {
			isWorkBuddy = true
		}
	}

	acc := &Account{
		ID:           baseName,
		Name:         baseName,
		FilePath:     filePath,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
	}

	if isAntigravity {
		acc.Type = TypeAntigravity
		acc.ProjectID = raw.ProjectID
		if acc.ProjectID == "" {
			acc.ProjectID = "aicode-consumers"
		}
		acc.Email = raw.Email
		if acc.Email != "" {
			acc.Name = fmt.Sprintf("Antigravity (%s)", acc.Email)
		}
		if raw.AntigravityCapabilityHints != nil && len(raw.AntigravityCapabilityHints.ModelIDs) > 0 {
			var ms []ModelMetadata
			for _, mid := range raw.AntigravityCapabilityHints.ModelIDs {
				ms = append(ms, ModelMetadata{
					ID:   mid,
					Name: mid,
				})
			}
			acc.SetModels(ms)
		}
	} else if isQoder {
		acc.Type = TypeQoder
		acc.MachineID = raw.MachineID
		acc.MachineToken = raw.MachineToken
		acc.AccountName = raw.AccountName
		acc.Email = raw.Email
		acc.Realm = raw.Realm
		if acc.Realm == "" {
			if strings.Contains(pathLower, "-cn") || strings.Contains(raw.Domain, "qoder.com.cn") {
				acc.Realm = "cn"
			} else {
				acc.Realm = "intl"
			}
		}
		switch {
		case acc.AccountName != "":
			acc.Name = fmt.Sprintf("Qoder (%s)", acc.AccountName)
		case acc.Email != "":
			acc.Name = fmt.Sprintf("Qoder (%s)", acc.Email)
		default:
			acc.Name = fmt.Sprintf("Qoder (%s - %s)", acc.Realm, baseName)
		}
		if len(raw.Models) > 0 {
			var ms []ModelMetadata
			for _, mid := range raw.Models {
				ms = append(ms, ModelMetadata{ID: mid, Name: mid})
			}
			acc.SetModels(ms)
		}
	} else {
		acc.Type = TypeWorkBuddy
		acc.UID = uid
		acc.Realm = raw.Realm
		if acc.Realm == "" {
			// The desktop files record the auth domain, which names the
			// region directly: workbuddy.cn is the domestic deployment.
			switch {
			case strings.Contains(domain, "workbuddy.ai"):
				acc.Realm = "intl"
			case strings.Contains(domain, "workbuddy.cn"):
				acc.Realm = "cn"
			default:
				acc.Realm = "cn"
			}
		}
		acc.Product = raw.Product
		if acc.Product == "" {
			acc.Product = "workbuddy"
		}
		acc.Domain = domain
		acc.EnterpriseID = raw.EnterpriseID
		if acc.EnterpriseID == "" {
			acc.EnterpriseID = raw.TenantIDCamel
		}
		switch {
		case raw.Account != nil && asString(raw.Account.Nickname) != "":
			acc.Name = fmt.Sprintf("WorkBuddy (%s)", asString(raw.Account.Nickname))
		case raw.Account != nil && asString(raw.Account.Phone) != "":
			acc.Name = fmt.Sprintf("WorkBuddy (%s)", asString(raw.Account.Phone))
		case acc.UID != "":
			acc.Name = fmt.Sprintf("WorkBuddy (%s - %s)", acc.Realm, acc.UID)
		}
		if len(raw.Models) > 0 {
			var ms []ModelMetadata
			for _, mid := range raw.Models {
				ms = append(ms, ModelMetadata{
					ID:   mid,
					Name: mid,
				})
			}
			acc.SetModels(ms)
		}
	}

	return acc, nil
}

// isQoderToken reports whether a credential carries one of the prefixes the
// Qoder platforms issue.
func isQoderToken(token string) bool {
	for _, prefix := range []string{"pt-", "jt-", "dt-", "drt-"} {
		if strings.HasPrefix(token, prefix) {
			return true
		}
	}
	return false
}

func SaveAccountToFile(acc *Account) error {
	if acc.FilePath == "" {
		return nil
	}

	var modelIDs []string
	for _, m := range acc.GetModels() {
		modelIDs = append(modelIDs, m.ID)
	}

	content := GenericOAuthFile{
		Type:         string(acc.Type),
		AccessToken:  acc.AccessToken,
		RefreshToken: acc.RefreshToken,
		ProjectID:    acc.ProjectID,
		Email:        acc.Email,
		UID:          acc.UID,
		Realm:        acc.Realm,
		Product:      acc.Product,
		Domain:       acc.Domain,
		EnterpriseID: acc.EnterpriseID,
		MachineID:    acc.MachineID,
		MachineToken: acc.MachineToken,
		AccountName:  acc.AccountName,
		Models:       modelIDs,
	}
	if !acc.ExpiresAt.IsZero() {
		content.Expired = acc.ExpiresAt.Format(time.RFC3339)
	}

	data, err := json.MarshalIndent(content, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(acc.FilePath, data, 0644)
}
