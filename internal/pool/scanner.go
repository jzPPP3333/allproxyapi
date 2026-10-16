package pool

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type GenericOAuthFile struct {
	Type         string `json:"type"` // "antigravity", "workbuddy", etc.
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Expired      string `json:"expired"` // RFC3339 string
	Timestamp    int64  `json:"timestamp"`

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

	// Resolve tokens from multiple schema variations
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

	if accessToken == "" && refreshToken == "" {
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
	}

	// Determine Account Type via heuristics
	pathLower := strings.ToLower(filePath)
	typeLower := strings.ToLower(raw.Type)

	isAntigravity := typeLower == "antigravity" || strings.Contains(pathLower, "antigravity") || raw.ProjectID != ""
	isQoder := !isAntigravity && (typeLower == "qoder" ||
		strings.Contains(pathLower, "qoder") ||
		isQoderToken(accessToken) || isQoderToken(refreshToken))
	isWorkBuddy := !isAntigravity && !isQoder &&
		(typeLower == "workbuddy" || strings.Contains(pathLower, "workbuddy") ||
			strings.Contains(pathLower, "codebuddy") || raw.UID != "" || raw.UserIDCamel != "")

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
		acc.UID = raw.UID
		if acc.UID == "" {
			acc.UID = raw.UserIDCamel
		}
		if acc.UID == "" {
			acc.UID = raw.UIDCamel
		}
		acc.Realm = raw.Realm
		if acc.Realm == "" {
			if strings.Contains(raw.Domain, "workbuddy.ai") {
				acc.Realm = "intl"
			} else {
				acc.Realm = "cn"
			}
		}
		acc.Product = raw.Product
		if acc.Product == "" {
			acc.Product = "workbuddy"
		}
		acc.Domain = raw.Domain
		acc.EnterpriseID = raw.EnterpriseID
		if acc.EnterpriseID == "" {
			acc.EnterpriseID = raw.TenantIDCamel
		}
		if acc.UID != "" {
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
