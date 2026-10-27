package harness

import (
	"crypto/sha256"
	"encoding/binary"
)

// WorkerRole identifies worker class for seed derivation.
type WorkerRole string

const (
	RoleReader       WorkerRole = "reader"
	RoleWriter       WorkerRole = "writer"
	RoleWarmupReader WorkerRole = "warmup_reader"
	RoleFlushBatch   WorkerRole = "flush_batch"
)

// DeriveWorkerSeed derives a deterministic 63-bit positive pseudorandom seed for a worker.
// The derivation is strictly a function of:
// - pairSeed: the common seed assigned to the matched A/B pair
// - role: worker class (reader, writer, warmup_reader, flush_batch)
// - workerIdx: worker ordinal (0, 1, 2, ...)
// - purpose: stream purpose ("keys", "hit_decision", "write_keys", "write_vals")
//
// Crucially, it DOES NOT depend on TrialIndex or SyncMode, guaranteeing bit-for-bit
// identical worker seeds between Approach A and Approach B in matched pairs.
func DeriveWorkerSeed(pairSeed int64, role WorkerRole, workerIdx int, purpose string) int64 {
	h := sha256.New()
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(pairSeed))
	h.Write(buf[:])
	h.Write([]byte(string(role)))
	binary.LittleEndian.PutUint64(buf[:], uint64(workerIdx))
	h.Write(buf[:])
	h.Write([]byte(purpose))
	digest := h.Sum(nil)

	// Take first 8 bytes as uint64, mask to positive int64
	val := binary.LittleEndian.Uint64(digest[:8])
	seed := int64(val & 0x7FFFFFFFFFFFFFFF)
	if seed == 0 {
		seed = 1 // Ensure non-zero
	}
	return seed
}
