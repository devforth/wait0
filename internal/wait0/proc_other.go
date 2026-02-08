//go:build !linux

package wait0

func processRSSBytes() (rssBytes uint64, ok bool) {
	return 0, false
}

func processSmapsRollupBytes() (vals map[string]uint64, ok bool) {
	return nil, false
}

func formatSmapsRollup(vals map[string]uint64) string {
	return ""
}
