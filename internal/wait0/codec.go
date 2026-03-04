package wait0

import (
	"bytes"
	"crypto/rand"
	"encoding/gob"
	"log"
	mrand "math/rand"
	"net/http"
	"time"
)

func encodeGob(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodeGob(b []byte, v any) error {
	dec := gob.NewDecoder(bytes.NewReader(b))
	return dec.Decode(v)
}

func randomString(n int) string {
	if n <= 0 {
		return ""
	}

	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	// Rejection sampling to avoid modulo bias.
	const max = byte(62 * 4) // 248

	out := make([]byte, 0, n)
	buf := make([]byte, n)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			for len(out) < n {
				out = append(out, alphabet[mrand.Intn(len(alphabet))])
			}
			break
		}
		for _, b := range buf {
			if len(out) >= n {
				break
			}
			if b >= max {
				continue
			}
			out = append(out, alphabet[int(b)%len(alphabet)])
		}
	}
	return string(out)
}

func init() {
	// Ensure http.Header is registered for gob.
	gob.Register(http.Header{})
	mrand.Seed(time.Now().UnixNano())
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
}
