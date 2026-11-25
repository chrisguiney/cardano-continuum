// Package rng turns one root seed into independent, stable random streams.
//
// A stream is named by a label path. Derive hashes the parent seed together
// with the path, so a stream depends only on (root, path): there is no shared
// counter, no global source, and adding a stream never shifts another. Output
// is fixed by HKDF-SHA256 and ChaCha8, both specified algorithms, so
// sequences do not depend on the Go version or platform.
//
// Changing derivationVersion invalidates every recorded run.
package rng

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"math/rand/v2"
)

// Seed is a 256-bit root or derived seed.
type Seed [32]byte

const derivationVersion = "continuum/rng/v1"

// Derive returns the child seed for path using HKDF-SHA256, with the
// derivation version as salt and a length-prefixed encoding of path as info.
// The length prefixes make paths unambiguous: ("a","b") differs from ("ab")
// and from ("a","b","").
func (s Seed) Derive(path ...string) Seed {
	info := binary.BigEndian.AppendUint32(nil, uint32(len(path)))
	for _, p := range path {
		info = binary.BigEndian.AppendUint64(info, uint64(len(p)))
		info = append(info, p...)
	}
	// hkdf.Key fails only for an oversized length or an invalid hash, and
	// neither can happen for a fixed 32-byte request on SHA-256.
	okm, err := hkdf.Key(sha256.New, s[:], []byte(derivationVersion), string(info), len(Seed{}))
	if err != nil {
		panic("rng: hkdf: " + err.Error())
	}
	var out Seed
	copy(out[:], okm)
	return out
}

// Rand returns a new generator seeded with s. Each call starts a fresh,
// identical sequence; the generator is not safe for concurrent use.
func (s Seed) Rand() *rand.Rand {
	return rand.New(rand.NewChaCha8(s))
}
