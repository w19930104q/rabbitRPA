package rpa

import (
	"crypto/rand"
	"encoding/binary"
	"io"
	mrand "math/rand"
)

const nameLetters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// randName returns a short random file-name fragment.
func randName(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is catastrophic for the host; fall back so the
		// engine still returns an error instead of panicking.
		for i := range b {
			b[i] = byte(mrand.Intn(len(nameLetters)))
		}
		return string(b)
	}
	for i := range b {
		b[i] = nameLetters[int(b[i])%len(nameLetters)]
	}
	return string(b)
}

// seededReader returns an deterministic reader for reproducible run
// directory names in tests.
func seededReader(seed int64) io.Reader {
	return &seededRand{r: mrand.New(mrand.NewSource(seed))}
}

type seededRand struct {
	r *mrand.Rand
}

func (s *seededRand) Read(p []byte) (int, error) {
	for i := 0; i < len(p); i += 8 {
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], s.r.Uint64())
		copy(p[i:], buf[:])
	}
	return len(p), nil
}
