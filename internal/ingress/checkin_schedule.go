package ingress

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

// CheckinScheduler runs the daily check-in once per day at a configured local
// hour. The last summary is retained so the dashboard can show what the most
// recent run did without triggering another.
type CheckinScheduler struct {
	server   *Server
	hour     int
	interval time.Duration

	mu       sync.RWMutex
	last     *pool.CheckinSummary
	lastDay  string
	stopChan chan struct{}
	stopOnce sync.Once
}

// NewCheckinScheduler builds a scheduler. hour is the local hour (0-23) to run
// at; an out-of-range value falls back to 9.
func NewCheckinScheduler(server *Server, hour int) *CheckinScheduler {
	if hour < 0 || hour > 23 {
		hour = 9
	}
	return &CheckinScheduler{
		server:   server,
		hour:     hour,
		interval: time.Minute,
		stopChan: make(chan struct{}),
	}
}

// Start begins the daily loop. The check runs on a one-minute tick because a
// sleep to the next occurrence would drift across a laptop suspend; the day
// guard makes the tick idempotent.
func (s *CheckinScheduler) Start() {
	go func() {
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				s.Tick()
			case <-s.stopChan:
				return
			}
		}
	}()
}

// Stop ends the loop. It is safe to call more than once.
func (s *CheckinScheduler) Stop() {
	s.stopOnce.Do(func() { close(s.stopChan) })
}

// Tick runs the check-in when the current local time matches the configured
// hour and no run has happened yet today. It is exported so a caller can drive
// the schedule deterministically in a test.
func (s *CheckinScheduler) Tick() {
	now := time.Now()
	if now.Hour() != s.hour {
		return
	}

	day := now.Format("2006-01-02")
	s.mu.RLock()
	done := s.lastDay == day
	s.mu.RUnlock()
	if done {
		return
	}

	s.RunNow()
}

// RunNow performs the check-in for every provider and records the summary. It
// marks the day as handled even when nothing was attempted, so a failing run
// does not retry on every tick for the rest of the hour.
func (s *CheckinScheduler) RunNow() pool.CheckinSummary {
	s.mu.Lock()
	s.lastDay = time.Now().Format("2006-01-02")
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	summary := s.server.RunCheckinAll(ctx)

	s.mu.Lock()
	s.last = &summary
	s.mu.Unlock()

	log.Printf("[Checkin] 每日签到完成: 尝试 %d, 成功 %d, 已签到 %d, 不支持 %d, 失败 %d",
		summary.Attempted, summary.Succeeded, summary.AlreadyDone, summary.Unsupported, summary.Failed)

	return summary
}

// Last returns the most recent summary, or nil when no run has happened.
func (s *CheckinScheduler) Last() *pool.CheckinSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.last
}
