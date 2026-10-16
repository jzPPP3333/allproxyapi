package pool

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

type RefreshHandler func(ctx context.Context, acc *Account) error

type Refresher struct {
	pool       *AccountPool
	interval   time.Duration
	handlers   map[AccountType]RefreshHandler
	handlersMu sync.RWMutex
	stopChan   chan struct{}
}

func NewRefresher(pool *AccountPool, interval time.Duration) *Refresher {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	return &Refresher{
		pool:     pool,
		interval: interval,
		handlers: make(map[AccountType]RefreshHandler),
		stopChan: make(chan struct{}),
	}
}

func (r *Refresher) RegisterHandler(t AccountType, h RefreshHandler) {
	r.handlersMu.Lock()
	defer r.handlersMu.Unlock()
	r.handlers[t] = h
}

func (r *Refresher) Start() {
	go func() {
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()

		// Run once on startup
		r.CheckAndRefreshAll()

		for {
			select {
			case <-ticker.C:
				r.CheckAndRefreshAll()
			case <-r.stopChan:
				return
			}
		}
	}()
}

func (r *Refresher) Stop() {
	close(r.stopChan)
}

func (r *Refresher) RefreshAccount(ctx context.Context, acc *Account) error {
	r.handlersMu.RLock()
	handler, ok := r.handlers[acc.Type]
	r.handlersMu.RUnlock()

	if !ok {
		return nil
	}

	// Collapse concurrent refreshes of the same account into one provider call.
	// Several requests noticing the same expiring token at once would otherwise
	// each refresh it, and a provider that rotates the refresh token on use
	// would leave all but the last response invalid.
	err := singleflightRefresh(acc, func() error {
		return handler(ctx, acc)
	})
	if err != nil {
		log.Printf("[Refresher] failed to refresh %s account '%s': %v", acc.Type, acc.Name, err)
		return err
	}

	log.Printf("[Refresher] Successfully refreshed %s account '%s' (expires: %s)",
		acc.Type, acc.Name, acc.ExpiresAt.Format(time.RFC3339))

	// Persist to file if it was loaded from a file
	if acc.FilePath != "" {
		if err := SaveAccountToFile(acc); err != nil {
			log.Printf("[Refresher] failed to save updated account to %s: %v", acc.FilePath, err)
		}
	}

	return nil
}

// hasRenewableCredential reports whether an account's access token is itself a
// renewable credential for its provider.
//
// A Qoder personal token is long-lived and is the exchange source for the
// short-lived token that actually gets used, but it lands in the access field
// on import, so RefreshToken is empty. Without this the refresher would skip
// those accounts entirely. The check is deliberately provider-scoped so no
// other provider's access token is treated as renewable.
func hasRenewableCredential(acc *Account) bool {
	return acc.Type == TypeQoder && strings.HasPrefix(acc.AccessToken, "pt-")
}

func (r *Refresher) CheckAndRefreshAll() {
	accounts := r.pool.List()
	now := time.Now()

	for _, acc := range accounts {
		// Renew when the credential is close to expiring.
		//
		// A credential with no refresh token is still renewable when it is a
		// Qoder personal token: the exchange source is the credential itself,
		// so requiring a refresh token here would park those accounts until
		// they were re-imported by hand.
		needsRefresh := false
		if acc.RefreshToken != "" || hasRenewableCredential(acc) {
			if acc.ExpiresAt.IsZero() || now.Add(10*time.Minute).After(acc.ExpiresAt) {
				needsRefresh = true
			}
		}

		if needsRefresh {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = r.RefreshAccount(ctx, acc)
			cancel()
		}
	}
}
