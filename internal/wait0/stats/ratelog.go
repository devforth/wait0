package stats

import (
	"log"
	"sync"
	"time"
)

type RateLimitedLogger struct {
	mu       sync.Mutex
	lastAt   time.Time
	interval time.Duration
}

func NewRateLimitedLogger(interval time.Duration) *RateLimitedLogger {
	return &RateLimitedLogger{interval: interval}
}

func (l *RateLimitedLogger) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if !l.lastAt.IsZero() && now.Sub(l.lastAt) < l.interval {
		return
	}
	l.lastAt = now
	log.Printf(format, args...)
}
