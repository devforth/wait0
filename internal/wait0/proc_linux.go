//go:build linux

package wait0

import (
	"bufio"
	"bytes"
	"os"
	"sort"
	"strconv"
	"strings"
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

// processSmapsRollupBytes reads /proc/self/smaps_rollup and returns the parsed
// fields (in bytes). This is best-effort: if unavailable or parsing fails, ok is false.
//
// The primary purpose is to split RSS into Anonymous/File/Shmem to distinguish
// heap/anon growth from file-backed mmaps.
func processSmapsRollupBytes() (vals map[string]uint64, ok bool) {
	f, err := os.Open("/proc/self/smaps_rollup")
	if err != nil {
		return nil, false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// The rollup file is small, but bump buffer to be safe.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	vals = make(map[string]uint64)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		// Format: "Key:    123 kB" (also sometimes without kB suffix).
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		rest := strings.TrimSpace(line[colon+1:])
		fields := strings.Fields(rest)
		if key == "" || len(fields) == 0 {
			continue
		}
		n, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		// Most values are in kB in smaps_rollup.
		vals[key] = n * 1024
	}
	if err := sc.Err(); err != nil {
		return nil, false
	}
	if len(vals) == 0 {
		return nil, false
	}
	return vals, true
}

func formatSmapsRollup(vals map[string]uint64) string {
	if len(vals) == 0 {
		return ""
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(formatBytes(vals[k]))
	}
	return b.String()
}
