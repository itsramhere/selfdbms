package lsmengine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newReadPathManager(t *testing.T, cfg Config) *Manager {
	t.Helper()
	cfg.DataDir = t.TempDir()
	cfg.WalPath = "wal.log"
	m, err := CreateManagerWithConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return m
}

func flushReadPathManager(t *testing.T, m *Manager) {
	t.Helper()
	if err := m.TriggerFlush(); err != nil {
		t.Fatal(err)
	}
	if err := m.WaitForFlushCompletion(); err != nil {
		t.Fatal(err)
	}
}

func readPathValue(key int, seq uint64) []byte {
	if seq == 11 && key == 2 {
		return []byte{}
	}
	return bytes.Repeat([]byte(fmt.Sprintf("key=%02d,seq=%02d;", key, seq)), 50)
}

func TestReadPathEquivalence(t *testing.T) {
	const keyCount, versions = 20, 20
	m := newReadPathManager(t, Config{})
	// Real transactions retain many versions in one memtable, then the real
	// flush path writes the sorted, variable-length blocks.
	for seq := uint64(1); seq <= versions; seq++ {
		tx := m.Begin()
		for k := 0; k < keyCount; k++ {
			key := fmt.Sprintf("key%03d", k*2)
			if (seq+uint64(k))%7 == 0 {
				if err := tx.Delete(key); err != nil {
					t.Fatal(err)
				}
			} else {
				tx.Put(key, string(readPathValue(k, seq)))
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	flushReadPathManager(t, m)
	block := m.ActiveSSreaders[0]
	legacy, err := NewSSTableReader(block.File.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if legacy.ReadPath != ReadPathBlock || legacy.ReadIODelay != 0 {
		t.Fatal("directly constructed reader did not use zero-value defaults")
	}
	legacy.ReadPath = ReadPathLegacyScan
	straddles := 0
	var previousLast []byte
	for i := range block.Index {
		data, err := block.readBlock(i, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		first, _, _ := DecodeInternalKey(block.Index[i].Key)
		if i > 0 && bytes.Equal(previousLast, first) {
			straddles++
		}
		for len(data) > 0 {
			var key []byte
			key, _, data, err = parseBlockRecord(data)
			if err != nil {
				t.Fatal(err)
			}
			previousLast, _, _ = DecodeInternalKey(key)
		}
	}
	if straddles == 0 {
		t.Fatal("fixture must contain versions straddling block boundaries")
	}
	t.Logf("%d blocks, %d boundaries straddled by one user key", len(block.Index), straddles)
	timestamps := []uint64{math.MaxUint64}
	for ts := uint64(0); ts <= versions+1; ts++ {
		timestamps = append(timestamps, ts)
	}
	keys := []string{"", "aaa", "zzz"}
	for k := 0; k <= keyCount*2; k++ {
		keys = append(keys, fmt.Sprintf("key%03d", k))
	}
	for _, key := range keys {
		for _, ts := range timestamps {
			got, gotErr := block.Get([]byte(key), ts)
			want, wantErr := legacy.Get([]byte(key), ts)
			if !bytes.Equal(got, want) || gotErr != wantErr {
				t.Fatalf("Get(%q, %d): block=(%q, %v), legacy=(%q, %v)", key, ts, got, gotErr, want, wantErr)
			}
		}
		got, gotErr := block.LatestValue([]byte(key))
		want, wantErr := legacy.LatestValue([]byte(key))
		if got != want || gotErr != wantErr {
			t.Fatalf("LatestValue(%q): block=(%d, %v), legacy=(%d, %v)", key, got, gotErr, want, wantErr)
		}
	}
	// Also check against the committed history so parity cannot hide a shared
	// visibility or tombstone error.
	for k := 0; k < keyCount; k++ {
		key := []byte(fmt.Sprintf("key%03d", k*2))
		for ts := uint64(0); ts <= versions+1; ts++ {
			seq := min(ts, uint64(versions))
			var want []byte
			var wantErr error
			switch {
			case seq == 0:
				wantErr = ErrNotFound
			case (seq+uint64(k))%7 == 0:
				wantErr = ErrKeyDeleted
			default:
				want = readPathValue(k, seq)
			}
			got, err := block.Get(key, ts)
			if !bytes.Equal(got, want) || err != wantErr {
				t.Fatalf("history Get(%q, %d): got (%q, %v), want (%q, %v)", key, ts, got, err, want, wantErr)
			}
		}
		if seq, err := block.LatestValue(key); seq != versions || err != nil {
			t.Fatalf("history LatestValue(%q) = (%d, %v)", key, seq, err)
		}
	}

	// Seeking just before a block's first non-tombstone at the same timestamp
	// must continue into that block from its predecessor.
	for i := 1; i < len(block.Index); i++ {
		key, seq, deleted := DecodeInternalKey(block.Index[i].Key)
		if deleted {
			continue
		}
		reads := 0
		block.blockReadHook = func() { reads++ }
		block.ReadIODelay = 2 * time.Millisecond
		start := hrNowNanos()
		_, err := block.Get(key, seq)
		elapsed := time.Duration(hrNowNanos() - start)
		block.blockReadHook = nil
		if err != nil || reads != 2 {
			t.Fatalf("boundary Get(%q, %d): reads=%d, err=%v; want two reads", key, seq, reads, err)
		}
		if elapsed < 2*block.ReadIODelay {
			t.Fatalf("two-block lookup took %v, want >= %v", elapsed, 2*block.ReadIODelay)
		}
		return
	}
	t.Fatal("fixture must have a non-tombstone at a later block's start")
}

func TestReadPathBlockIOAndDelay(t *testing.T) {
	m := newReadPathManager(t, Config{ReadIODelay: 2 * time.Millisecond})
	tx := m.Begin()
	for k := 0; k < 40; k++ {
		tx.Put(fmt.Sprintf("key%03d", k*2), string(bytes.Repeat([]byte{'x'}, 700)))
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	flushReadPathManager(t, m)
	r := m.ActiveSSreaders[0]
	var reads atomic.Int32
	r.blockReadHook = func() { reads.Add(1) }
	for _, latest := range []bool{false, true} {
		lookup := func(key string) error {
			if latest {
				_, err := r.LatestValue([]byte(key))
				return err
			}
			_, err := r.Get([]byte(key), 100)
			return err
		}
		reads.Store(0)
		start := hrNowNanos()
		if err := lookup("key002"); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Duration(hrNowNanos() - start); elapsed < r.ReadIODelay {
			t.Fatalf("latest=%v: hit took %v, want >= %v", latest, elapsed, r.ReadIODelay)
		}
		if n := reads.Load(); n != 1 {
			t.Fatalf("latest=%v: hit read %d blocks, want 1", latest, n)
		}
		for _, key := range []string{"aaa", "zzz"} {
			reads.Store(0)
			// Use the fastest of several calls so an unrelated scheduler pause
			// cannot turn the no-delay assertion into a timing flake.
			fastest := time.Hour
			for attempt := 0; attempt < 10; attempt++ {
				start := hrNowNanos()
				err := lookup(key)
				fastest = min(fastest, time.Duration(hrNowNanos()-start))
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("range miss: %v", err)
				}
			}
			if n := reads.Load(); n != 0 || fastest >= r.ReadIODelay {
				t.Fatalf("latest=%v: range miss read %d blocks, fastest=%v", latest, n, fastest)
			}
		}
		// An absent key in the first block must stop there, rather than
		// reading every later block in the data region.
		reads.Store(0)
		if err := lookup("key001"); err != ErrNotFound || reads.Load() != 1 {
			t.Fatalf("latest=%v: gap miss err=%v, reads=%d", latest, err, reads.Load())
		}
		// Legacy scans bypass the block hook and delay even for in-range hits.
		r.ReadPath = ReadPathLegacyScan
		reads.Store(0)
		fastest := time.Hour
		for attempt := 0; attempt < 10; attempt++ {
			start := hrNowNanos()
			err := lookup("key002")
			fastest = min(fastest, time.Duration(hrNowNanos()-start))
			if err != nil {
				t.Fatal(err)
			}
		}
		if reads.Load() != 0 || fastest >= r.ReadIODelay {
			t.Fatalf("latest=%v: legacy scan used block delay: reads=%d, fastest=%v", latest, reads.Load(), fastest)
		}
		r.ReadPath = ReadPathBlock
	}
	// No descriptor access is needed for range misses, even on a closed file.
	standalone, err := NewSSTableReader(r.File.Name())
	if err != nil {
		t.Fatal(err)
	}
	if err := standalone.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := standalone.Get([]byte("zzz"), 100); err != ErrNotFound {
		t.Fatalf("closed out-of-range Get: %v", err)
	}
	if _, err := standalone.LatestValue([]byte("aaa")); err != ErrNotFound {
		t.Fatalf("closed out-of-range LatestValue: %v", err)
	}
	if _, err := standalone.Get([]byte("key002"), 100); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed in-range Get: %v", err)
	}
}

func TestReadPathManagerWiring(t *testing.T) {
	for _, path := range []ReadPathMode{ReadPathBlock, ReadPathLegacyScan} {
		for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachAMin, SyncModeApproachB} {
			t.Run(fmt.Sprintf("path%d/%s", path, mode), func(t *testing.T) {
				const delay = 50 * time.Microsecond
				m := newReadPathManager(t, Config{Mode: mode, ReadPath: path, ReadIODelay: delay})
				for round := 0; round < 2; round++ {
					tx := m.Begin()
					tx.Put("key", fmt.Sprintf("value%d", round))
					tx.Put(fmt.Sprintf("other%d", round), "other")
					if err := tx.Commit(); err != nil {
						t.Fatal(err)
					}
					flushReadPathManager(t, m)
				}
				check := func(count int) {
					t.Helper()
					readers := m.ActiveSSreaders
					if mode == SyncModeApproachB {
						snapshot, err := m.AcquireSnapshot()
						if err != nil {
							t.Fatal(err)
						}
						defer m.ReleaseSnapshot(snapshot)
						for _, shared := range snapshot.Readers() {
							readers = append(readers, shared.Reader)
						}
					}
					if len(readers) != count {
						t.Fatalf("readers=%d, want %d", len(readers), count)
					}
					for _, r := range readers {
						if r.ReadPath != path || r.ReadIODelay != delay {
							t.Fatalf("reader configuration: path=%v, delay=%v", r.ReadPath, r.ReadIODelay)
						}
					}
					for _, query := range []struct {
						key, value string
						ts         uint64
					}{
						{"key", "value1", 2}, {"other0", "other", 2}, {"other1", "other", 2},
					} {
						if value, err := m.Get(query.key, query.ts); value != query.value || err != nil {
							t.Fatalf("Get(%q, %d)=(%q, %v), want %q", query.key, query.ts, value, err, query.value)
						}
					}
					if seq, err := m.LatestValue([]byte("key")); seq != 2 || err != nil {
						t.Fatalf("LatestValue=(%d, %v)", seq, err)
					}
				}
				check(2)
				if value, err := m.Get("key", 1); value != "value0" || err != nil {
					t.Fatalf("before compaction Get(key, 1)=(%q, %v)", value, err)
				}
				if err := m.CompactAll(); err != nil {
					t.Fatal(err)
				}
				check(1)
			})
		}
	}
}

func TestReadPathConcurrentReads(t *testing.T) {
	m := newReadPathManager(t, Config{})
	tx := m.Begin()
	for i := 0; i < 40; i++ {
		tx.Put(fmt.Sprintf("key%03d", i), string(bytes.Repeat([]byte{byte(i)}, 700)))
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	flushReadPathManager(t, m)
	r := m.ActiveSSreaders[0]
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			for k := 0; k < 40; k++ {
				value, err := r.Get([]byte(fmt.Sprintf("key%03d", k)), 100)
				if !bytes.Equal(value, bytes.Repeat([]byte{byte(k)}, 700)) || err != nil {
					t.Errorf("concurrent Get(%d): err=%v", k, err)
				}
			}
		})
	}
	wg.Wait()
}

func TestWALSyncRecovery(t *testing.T) {
	var syncedBytes []byte
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("disabled=%v", disabled), func(t *testing.T) {
			m := newReadPathManager(t, Config{DisableWALSync: disabled})
			if m.Log.DisableSync.Load() != disabled {
				t.Fatal("initial WAL sync setting not propagated")
			}
			tx := m.Begin()
			tx.Put("key", "old")
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			flushReadPathManager(t, m)
			if m.Log.DisableSync.Load() != disabled {
				t.Fatal("manual rotation lost WAL sync setting")
			}
			// Exercise threshold-triggered rotation too.
			m.memtableThresholdBytes = 1
			tx = m.Begin()
			tx.Put("threshold", "flush")
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := m.WaitForFlushCompletion(); err != nil {
				t.Fatal(err)
			}
			if m.Log.DisableSync.Load() != disabled {
				t.Fatal("automatic rotation lost WAL sync setting")
			}
			// Write a deterministic record sequence so byte-for-byte parity can
			// be checked independently of map iteration order in transactions.
			path := m.Log.File.Name()
			var expected bytes.Buffer
			for _, record := range []struct {
				key     string
				seq     uint64
				deleted bool
				value   string
			}{
				{"key", 3, false, "new"}, {"gone", 4, false, "old"},
				{"gone", 5, true, ""}, {"empty", 6, false, ""},
			} {
				key := EncodeInternalKey([]byte(record.key), record.seq, record.deleted)
				if err := m.Log.Append(key, []byte(record.value)); err != nil {
					t.Fatal(err)
				}
				// Historical checksum order differs from the disk header order.
				var payload bytes.Buffer
				binary.Write(&payload, binary.LittleEndian, uint16(len(key)))
				payload.Write(key)
				binary.Write(&payload, binary.LittleEndian, uint32(len(record.value)))
				payload.WriteString(record.value)
				binary.Write(&expected, binary.LittleEndian, crc32.ChecksumIEEE(payload.Bytes()))
				binary.Write(&expected, binary.LittleEndian, uint16(len(key)))
				binary.Write(&expected, binary.LittleEndian, uint32(len(record.value)))
				expected.Write(key)
				expected.WriteString(record.value)
			}
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, expected.Bytes()) {
				t.Fatal("WAL bytes differ from historical record format")
			}
			if !disabled {
				syncedBytes = bytes.Clone(data)
			} else if !bytes.Equal(data, syncedBytes) {
				t.Fatal("WAL bytes differ between sync settings")
			}
			// Reopen through the public constructor and recover from the log.
			recovered, err := CreateManagerWithConfig(Config{
				DataDir: filepath.Dir(path), WalPath: path, DisableWALSync: disabled,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close()
			if err := recovered.RecoverFromWAL(path); err != nil {
				t.Fatal(err)
			}
			for _, query := range []struct {
				key, value string
				ts         uint64
				err        error
			}{
				{"key", "new", 6, nil}, {"gone", "old", 4, nil},
				{"gone", "", 6, ErrKeyDeleted}, {"empty", "", 6, nil},
				{"missing", "", 6, ErrNotFound},
			} {
				value, err := recovered.Get(query.key, query.ts)
				if err != query.err || (err == nil && value != query.value) {
					t.Fatalf("recovered Get(%q, %d)=(%q, %v)", query.key, query.ts, value, err)
				}
			}
			if recovered.CurrentSeqNo.Load() != 6 {
				t.Fatal("recovery lost sequence number")
			}
		})
	}
}
