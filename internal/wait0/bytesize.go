package wait0

import (
	"fmt"
	"strconv"
	"strings"
)

func parseBytes(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	mult := int64(1)
	last := s[len(s)-1]
	if last == 'b' {
		s = strings.TrimSpace(s[:len(s)-1])
		if s == "" {
			return 0, fmt.Errorf("invalid size")
		}
		last = s[len(s)-1]
	}
	switch last {
	case 'k':
		mult = 1024
		s = s[:len(s)-1]
	case 'm':
		mult = 1024 * 1024
		s = s[:len(s)-1]
	case 'g':
		mult = 1024 * 1024 * 1024
		s = s[:len(s)-1]
	}
	s = strings.TrimSpace(s)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if v < 0 {
		return 0, fmt.Errorf("negative size")
	}
	return int64(v * float64(mult)), nil
}
