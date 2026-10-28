package harness2

// H-11: deterministic logical request streams.
//
// Every request is a pure function of (triplet seed, operation index):
//
//	key_j    = f(seed, "key", j)
//	hit_j    = f(seed, "hit", j) < hitRatio
//	absent_j = keyspace + f(seed, "miss", j) mod 10000
//
// Therefore request j is bit-identical in every arm of a triplet, including its
// key and whether it is a hit, regardless of how many requests each arm
// completed. The frozen harness derived keys from a per-worker PRNG stream, so
// paired arms diverged as soon as their op counts differed (audit IA-8).

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// KeyStream produces the deterministic logical request stream.
type KeyStream struct {
	seed     int64
	keyspace int
	hitBP    uint64 // hit ratio in basis points (0..10000)
}

// NewKeyStream builds a stream for one triplet.
func NewKeyStream(seed int64, keyspace int, hitRatio float64) *KeyStream {
	if keyspace <= 0 {
		keyspace = 1
	}
	bp := uint64(hitRatio * 10000)
	if bp > 10000 {
		bp = 10000
	}
	return &KeyStream{seed: seed, keyspace: keyspace, hitBP: bp}
}

func (k *KeyStream) digest(tag byte, index int64) [32]byte {
	var buf [17]byte
	binary.LittleEndian.PutUint64(buf[0:8], uint64(k.seed))
	buf[8] = tag
	binary.LittleEndian.PutUint64(buf[9:17], uint64(index))
	return sha256.Sum256(buf[:])
}

// At returns the key and expected-hit flag for a logical request index.
func (k *KeyStream) At(index int64) (string, bool) {
	h := k.digest('k', index)
	raw := binary.LittleEndian.Uint64(h[0:8])
	hitDraw := binary.LittleEndian.Uint64(h[8:16]) % 10000
	missDraw := binary.LittleEndian.Uint64(h[16:24])

	if hitDraw < k.hitBP {
		return formatKey(int(raw % uint64(k.keyspace))), true
	}
	// Guaranteed-absent key: strictly outside the populated range.
	idx := k.keyspace + 1 + int(missDraw%10000)
	return formatKey(idx), false
}

// KeyCount exposes the populated keyspace size (tests).
func (k *KeyStream) KeyCount() int { return k.keyspace }

func formatKey(idx int) string { return fmt.Sprintf("user_key_%010d", idx) }
