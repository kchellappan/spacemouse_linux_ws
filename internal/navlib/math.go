package navlib

import (
	"crypto/rand"
	"math"
)

func sqrt(v float64) float64 { return math.Sqrt(v) }

// shortID returns a short random identifier for connexion/instance ids.
func shortID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}
