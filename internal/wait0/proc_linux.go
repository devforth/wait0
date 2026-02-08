//go:build linux

package wait0

import (
	"bytes"
	"os"
	"strconv"
)

// processRSSBytes returns the process resident set size (RSS) in bytes.
// It is best-effort: if /proc is unavailable or parsing fails, ok is false.
func processRSSBytes() (rssBytes uint64, ok bool) {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, false
	}
	fields := bytes.Fields(b)
	if len(fields) < 2 {
		return 0, false
	}
	rssPages, err := strconv.ParseUint(string(fields[1]), 10, 64)
	if err != nil {
		return 0, false
	}
	return rssPages * uint64(os.Getpagesize()), true
}
