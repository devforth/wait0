//go:build !linux

package stats

func ProcessRSSBytes() (rssBytes uint64, ok bool) {
	return 0, false
}

func ProcessSmapsRollupBytes() (vals map[string]uint64, ok bool) {
	return nil, false
}

func FormatSmapsRollup(vals map[string]uint64) string {
	return ""
}
