package harness2

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"owndbms/lsmengine"
)

func TestRealismConflictAccounting(t *testing.T) {
	s := Spec{Block: "X1", Cell: "D1_r04_w2_direct", ReadPath: "direct", ReaderConcurrency: 4, WriterConcurrency: 2}
	sh := &Shard{}
	recordUnpacedWriteFailure(sh, s, lsmengine.ErrWriteConflict)
	recordUnpacedWriteFailure(sh, s, errors.New("unrelated I/O error"))
	if sh.WritesError != 2 || sh.WriteConflictAborts != 1 {
		t.Fatalf("%+v", sh)
	}
	if tot := Sum([]*Shard{sh, {WriteConflictAborts: 2, WritesError: 2}}); tot.WriteConflictAborts != 3 || tot.WritesError != 4 {
		t.Fatalf("%+v", tot)
	}
}

func TestRealismSpecAndReadPath(t *testing.T) {
	for _, block := range []string{"X1", "X2", "X3", "X4", "E1", "SMOKE"} {
		s := Spec{Block: block, ReadPath: "direct"}
		path, err := s.ResolveReadPath()
		if (err == nil) != s.IsX() || err == nil && path != lsmengine.ReadPathDirect {
			t.Fatalf("%s: %v", block, err)
		}
		s.ReadIODelayUs = 1
		if _, err := s.ResolveReadPath(); err == nil {
			t.Fatal("direct injected delay accepted")
		}
	}
	s := Spec{Block: "X1", Cell: "D1_r04_w2_direct", ReadPath: "direct", ReaderConcurrency: 4, WriterConcurrency: 2, WalSync: true}
	if _, err := s.ResolveMode(); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*Spec){func(s *Spec) { s.Cell = "X1_r04_d0000" }, func(s *Spec) { s.ReadPath = "block" }, func(s *Spec) { s.WriterConcurrency = 3 }} {
		bad := s
		edit(&bad)
		if _, err := bad.ResolveMode(); err == nil {
			t.Fatal("noncampaign multiwriter X1 accepted")
		}
	}
	b, _ := json.Marshal(OperationCounts{})
	var fields map[string]any
	json.Unmarshal(b, &fields)
	if _, ok := fields["write_conflict_aborts"]; ok {
		t.Fatal("older JSON changed")
	}
}

func TestRealismPairedStreams(t *testing.T) {
	// The count/path does not enter request construction. Readers use their
	// original stride; writers partition indices, not the hashed key domain.
	for _, writers := range []int{1, 2, 4} {
		baseline := NewKeyStream(20261111, 10000, .95)
		comparison := NewKeyStream(20261111, 10000, .95)
		for i := int64(0); i < 1000; i++ {
			k, h := baseline.At(i)
			ck, ch := comparison.At(i)
			if k != ck || h != ch {
				t.Fatalf("stream differs at writers=%d,index=%d", writers, i)
			}
		}
	}
}

func TestRealismWriterStrideKeyCollision(t *testing.T) {
	// Regression evidence for the reported preregistration conflict. Disjoint
	// logical indices do NOT imply disjoint keys after KeyStream.At hashing.
	for _, writers := range []int{2, 4} {
		// pairedSeed(20261111, "D_r04", 1), using the production XOR for writes.
		stream := NewKeyStream(3507978258608147492^0x5f5f5f5f, 10000, 1)
		a, _ := stream.At(40)
		b, _ := stream.At(97)
		if a != b || a != "user_key_0000009160" {
			t.Fatal("registered collision example changed")
		}
		seen := map[string]int{}
		collision := false
		for i := int64(0); i < 10000; i++ {
			key, _ := stream.At(i)
			worker := int(i % int64(writers))
			if prior, ok := seen[key]; ok && prior != worker {
				collision = true
				break
			}
			seen[key] = worker
		}
		if !collision {
			t.Fatal("expected deterministic cross-writer key collision")
		}
	}
}

func TestRealismWriterCommitCountOnce(t *testing.T) {
	for _, writers := range []int{1, 2, 4} {
		m, err := lsmengine.CreateManagerWithConfig(lsmengine.Config{DataDir: t.TempDir(), DisableWALSync: true})
		if err != nil {
			t.Fatal(err)
		}
		var shards []*Shard
		// Deterministic single commits, no Run, timers or trial window. A stop
		// on acquisition permits the current commit to finish exactly once.
		for worker := 0; worker < writers; worker++ {
			stop := make(chan struct{})
			obs := &stopOnCommitLock{SyncCollector: NewSyncHistogramCollector(), stop: stop}
			if err := m.SetSyncObserver(obs, qpcElapsedNanos); err != nil {
				t.Fatal(err)
			}
			sh := &Shard{}
			s := Spec{Block: "X1", Cell: "D1_r04_w2_direct", ReadPath: "direct", ReaderConcurrency: 4, WriterConcurrency: writers}
			unpacedWriter(m, sh, stop, Now(), int64(time.Hour), s, NewKeyStream(20261111, 10000, 1), worker)
			shards = append(shards, sh)
		}
		tot := Sum(shards)
		if tot.WritesCompleted != int64(writers) || tot.WritesCompletedInWindow != int64(writers) || tot.WritesError != 0 || m.CurrentSeqNo.Load() != uint64(writers) {
			t.Fatalf("double/missing commits: %+v", tot)
		}
		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
