//go:build !linux

package wait0

func processRSSBytes() (rssBytes uint64, ok bool) {
	return 0, false
}
