package pool

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type AccountPool struct {
	mu       sync.RWMutex
	accounts map[string]*Account        // ID -> Account
	byType   map[AccountType][]*Account // Type -> list of Accounts
	cursor   map[AccountType]int        // Type -> Round-robin cursor
}

func NewAccountPool() *AccountPool {
	return &AccountPool{
		accounts: make(map[string]*Account),
		byType:   make(map[AccountType][]*Account),
		cursor:   make(map[AccountType]int),
	}
}

func (p *AccountPool) AddOrUpdate(acc *Account) {
	p.mu.Lock()
	defer p.mu.Unlock()

	existing, exists := p.accounts[acc.ID]
	if exists {
		// Update tokens & metadata on existing instance
		existing.SetTokens(acc.AccessToken, acc.RefreshToken, acc.ExpiresAt)
		existing.Name = acc.Name
		if acc.ProjectID != "" {
			existing.ProjectID = acc.ProjectID
		}
		if acc.UID != "" {
			existing.UID = acc.UID
		}
		if acc.Realm != "" {
			existing.Realm = acc.Realm
		}
		if acc.Product != "" {
			existing.Product = acc.Product
		}
		if acc.FilePath != "" {
			existing.FilePath = acc.FilePath
		}
		return
	}

	p.accounts[acc.ID] = acc
	p.byType[acc.Type] = append(p.byType[acc.Type], acc)
}

func (p *AccountPool) Remove(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	acc, exists := p.accounts[id]
	if !exists {
		return
	}
	delete(p.accounts, id)

	// Remove from byType slice
	list := p.byType[acc.Type]
	newList := make([]*Account, 0, len(list))
	for _, item := range list {
		if item.ID != id {
			newList = append(newList, item)
		}
	}
	p.byType[acc.Type] = newList
}

func (p *AccountPool) Get(id string) *Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.accounts[id]
}

func (p *AccountPool) SetAccountModels(id string, models []ModelMetadata) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if acc, ok := p.accounts[id]; ok {
		acc.SetModels(models)
	}
}

func (p *AccountPool) List() []*Account {
	p.mu.RLock()
	defer p.mu.RUnlock()

	res := make([]*Account, 0, len(p.accounts))
	for _, acc := range p.accounts {
		res = append(res, acc.Clone())
	}
	return res
}

func (p *AccountPool) ListByType(accType AccountType) []*Account {
	p.mu.RLock()
	defer p.mu.RUnlock()

	list := p.byType[accType]
	res := make([]*Account, len(list))
	for i, acc := range list {
		res[i] = acc.Clone()
	}
	return res
}

// Pick selects a healthy account using round-robin.
// If all accounts are in cooldown, it returns the one whose cooldown expires earliest.
func (p *AccountPool) Pick(accType AccountType) *Account {
	p.mu.Lock()
	defer p.mu.Unlock()

	list := p.byType[accType]
	if len(list) == 0 {
		return nil
	}

	n := len(list)

	// Try finding a healthy account (not in cooldown and not expired)
	for i := 0; i < n; i++ {
		idx := p.cursor[accType] % n
		p.cursor[accType] = (p.cursor[accType] + 1) % n
		acc := list[idx]

		if !acc.IsCoolingDown() && !acc.IsExpired() {
			return acc
		}
	}

	// Secondary check: allow expired token if it has refresh token (will be refreshed on demand)
	for i := 0; i < n; i++ {
		idx := p.cursor[accType] % n
		p.cursor[accType] = (p.cursor[accType] + 1) % n
		acc := list[idx]

		if !acc.IsCoolingDown() && acc.RefreshToken != "" {
			return acc
		}
	}

	// Fallback: pick the one whose cooldown expires earliest
	best := list[0]
	for _, acc := range list[1:] {
		if acc.CooldownUntil.Before(best.CooldownUntil) {
			best = acc
		}
	}
	return best
}

func (p *AccountPool) MarkCooldown(acc *Account, dur time.Duration, reason string) {
	if acc == nil {
		return
	}
	acc.SetCooldown(dur, reason)
}

func (p *AccountPool) ResetCooldown(id string) {
	p.mu.RLock()
	acc := p.accounts[id]
	p.mu.RUnlock()
	if acc != nil {
		acc.ResetCooldown()
	}
}

func (p *AccountPool) Stats() map[string]any {
	p.mu.RLock()
	defer p.mu.RUnlock()

	byType := make(map[string]map[string]int)
	stats := map[string]any{
		"total_accounts": len(p.accounts),
		"by_type":        byType,
	}

	for _, acc := range p.accounts {
		t := string(acc.Type)
		sub, ok := byType[t]
		if !ok {
			sub = map[string]int{"total": 0, "healthy": 0, "cooldown": 0, "expired": 0}
			byType[t] = sub
		}
		sub["total"]++
		sub[acc.Status()]++
	}

	return stats
}

func (p *AccountPool) Count(accType AccountType) int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.byType[accType])
}

func (p *AccountPool) String() string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	parts := make([]string, 0, len(p.byType))
	for t, list := range p.byType {
		if len(list) > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", t, len(list)))
		}
	}
	sort.Strings(parts)
	return fmt.Sprintf("AccountPool(total=%d, %s)", len(p.accounts), strings.Join(parts, ", "))
}
