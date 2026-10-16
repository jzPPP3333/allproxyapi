package pool

import (
	"context"
	"log"
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

	err := handler(ctx, acc)
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

func (r *Refresher) CheckAndRefreshAll() {
	accounts := r.pool.List()
	now := time.Now()

	for _, acc := range accounts {
		// Needs refresh if expiring within 10 minutes or already expired (if has refresh token)
		needsRefresh := false
		if acc.RefreshToken != "" {
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
