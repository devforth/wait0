package stats

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

func TestRateLimitedLogger_Printf(t *testing.T) {
	var buf bytes.Buffer
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	}()

	rl := NewRateLimitedLogger(40 * time.Millisecond)
	rl.Printf("line-1")
	rl.Printf("line-2")

	if got := strings.Count(buf.String(), "line-"); got != 1 {
		t.Fatalf("log count = %d, want 1; buf=%q", got, buf.String())
	}

	time.Sleep(50 * time.Millisecond)
	rl.Printf("line-3")
	if got := strings.Count(buf.String(), "line-"); got != 2 {
		t.Fatalf("log count = %d, want 2; buf=%q", got, buf.String())
	}
}
