package wait0

import (
	"log"
	"sync"
	"time"
)

type rateLimitedLogger struct {
	mu       sync.Mutex
	lastAt   time.Time
	interval time.Duration
}

func newRateLimitedLogger(interval time.Duration) *rateLimitedLogger {
	return &rateLimitedLogger{interval: interval}
}

func (l *rateLimitedLogger) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if !l.lastAt.IsZero() && now.Sub(l.lastAt) < l.interval {
		return
	}
	l.lastAt = now
	log.Printf(format, args...)
}
