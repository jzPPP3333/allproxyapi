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
	byType   map[AccountType][]*Account // Type -> list of accounts
	cursor   map[AccountType]int        // Type -> round-robin cursor
	affinity *affinityTracker           // conversation -> account bindings
}

func NewAccountPool() *AccountPool {
	return newAccountPoolWithSeed(time.Now().UnixNano())
}

// newAccountPoolWithSeed builds a pool whose random choices are reproducible,
// for tests that assert on selection.
func newAccountPoolWithSeed(seed int64) *AccountPool {
	return &AccountPool{
		accounts: make(map[string]*Account),
		byType:   make(map[AccountType][]*Account),
		cursor:   make(map[AccountType]int),
		affinity: newAffinityTracker(seed),
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

// Pick selects an account for a provider with no model constraint. Prefer
// PickFor when the request's model is known, so model capability and per-model
// cooldowns are respected.
func (p *AccountPool) Pick(accType AccountType) *Account {
	return p.PickFor(DispatchOptions{Provider: accType})
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
			// Every bucket is pre-created so the panel can read a count without
			// handling a missing key.
			sub = map[string]int{
				"total": 0, "healthy": 0,
				"cooling": 0, "frozen": 0, "dead": 0, "expired": 0,
				// Lifecycle states, reported alongside the display names so a
				// consumer can use either.
				"active": 0, "soft_cooldown": 0, "hard_cooldown": 0, "disabled": 0,
			}
			byType[t] = sub
		}
		sub["total"]++
		sub[acc.Status()]++
		sub[string(acc.State())]++
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
