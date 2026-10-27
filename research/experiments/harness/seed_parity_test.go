package harness

import (
	"fmt"
	"math/rand"
	"testing"
)

// TestWorkerSeedDerivation_ParityAcrossTrialIndices verifies that matched pairs
// with the same PairSeed receive bit-for-bit identical worker seeds regardless of TrialIndex.
func TestWorkerSeedDerivation_ParityAcrossTrialIndices(t *testing.T) {
	pairSeed := int64(2026102712)

	// Mode A trial (e.g. index 10) vs Mode B trial (e.g. index 11)
	readers := 8
	writers := 3

	// Derive seeds for Mode A
	seedsA := make(map[string]int64)
	for r := 0; r < readers; r++ {
		seedsA[fmt.Sprintf("reader_%d", r)] = DeriveWorkerSeed(pairSeed, RoleReader, r, "keys")
		seedsA[fmt.Sprintf("warmup_%d", r)] = DeriveWorkerSeed(pairSeed, RoleWarmupReader, r, "keys")
	}
	for w := 0; w < writers; w++ {
		seedsA[fmt.Sprintf("writer_%d", w)] = DeriveWorkerSeed(pairSeed, RoleWriter, w, "keys")
	}
	seedsA["flush"] = DeriveWorkerSeed(pairSeed, RoleFlushBatch, 0, "keys")

	// Derive seeds for Mode B (under identical pairSeed)
	seedsB := make(map[string]int64)
	for r := 0; r < readers; r++ {
		seedsB[fmt.Sprintf("reader_%d", r)] = DeriveWorkerSeed(pairSeed, RoleReader, r, "keys")
		seedsB[fmt.Sprintf("warmup_%d", r)] = DeriveWorkerSeed(pairSeed, RoleWarmupReader, r, "keys")
	}
	for w := 0; w < writers; w++ {
		seedsB[fmt.Sprintf("writer_%d", w)] = DeriveWorkerSeed(pairSeed, RoleWriter, w, "keys")
	}
	seedsB["flush"] = DeriveWorkerSeed(pairSeed, RoleFlushBatch, 0, "keys")

	// Verify exact bit-for-bit equality
	for k, sA := range seedsA {
		sB, exists := seedsB[k]
		if !exists {
			t.Fatalf("seed key %s missing from Mode B", k)
		}
		if sA != sB {
			t.Fatalf("seed mismatch for worker %s: Mode A had %d, Mode B had %d", k, sA, sB)
		}
		if sA <= 0 {
			t.Fatalf("seed for worker %s must be positive, got %d", k, sA)
		}
	}
}

// TestWorkerSeedDerivation_StreamParity verifies that workers in Mode A and Mode B
// consume the EXACT same pseudo-random sequence of keys and hit decisions.
func TestWorkerSeedDerivation_StreamParity(t *testing.T) {
	pairSeed := int64(987654321)
	cardinality := 10000
	hitRatio := 0.95
	draws := 10000

	for workerIdx := 0; workerIdx < 4; workerIdx++ {
		seedA := DeriveWorkerSeed(pairSeed, RoleReader, workerIdx, "keys")
		seedB := DeriveWorkerSeed(pairSeed, RoleReader, workerIdx, "keys")

		samplerA := NewKeySampler(seedA, cardinality, hitRatio, "uniform", 0.99)
		samplerB := NewKeySampler(seedB, cardinality, hitRatio, "uniform", 0.99)

		for d := 0; d < draws; d++ {
			kA, hitA := samplerA.SampleKey()
			kB, hitB := samplerB.SampleKey()

			if kA != kB {
				t.Fatalf("worker %d draw %d key mismatch: Mode A %s vs Mode B %s", workerIdx, d, kA, kB)
			}
			if hitA != hitB {
				t.Fatalf("worker %d draw %d hit decision mismatch: Mode A %v vs Mode B %v", workerIdx, d, hitA, hitB)
			}
		}
	}
}

// TestWorkerSeedDerivation_IndependentPairsDifferent verifies that different pair seeds
// produce statistically independent worker streams.
func TestWorkerSeedDerivation_IndependentPairsDifferent(t *testing.T) {
	seed1 := DeriveWorkerSeed(1001, RoleReader, 0, "keys")
	seed2 := DeriveWorkerSeed(1002, RoleReader, 0, "keys")

	if seed1 == seed2 {
		t.Fatalf("different pair seeds produced identical worker seed: %d", seed1)
	}

	// Verify writer seed is different from reader seed within same pair
	writerSeed := DeriveWorkerSeed(1001, RoleWriter, 0, "keys")
	if seed1 == writerSeed {
		t.Fatalf("reader and writer received identical worker seed within pair 1001: %d", seed1)
	}
}

// TestWorkerSeedDerivation_DeterministicSequenceDraws verifies that writer PRNG draws
// match bit-for-bit across paired configurations.
func TestWorkerSeedDerivation_DeterministicSequenceDraws(t *testing.T) {
	pairSeed := int64(55555555)
	wA := rand.New(rand.NewSource(DeriveWorkerSeed(pairSeed, RoleWriter, 1, "keys")))
	wB := rand.New(rand.NewSource(DeriveWorkerSeed(pairSeed, RoleWriter, 1, "keys")))

	for i := 0; i < 5000; i++ {
		drawA := wA.Intn(10000)
		drawB := wB.Intn(10000)
		if drawA != drawB {
			t.Fatalf("draw %d mismatch between paired writer generators: %d vs %d", i, drawA, drawB)
		}
	}
}
