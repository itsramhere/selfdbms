package lsmengine

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"unsafe"
)

func TestDirectAlignment(t *testing.T) {
	for _, c := range []struct {
		start        int64
		size         int
		offset       int64
		length, skip int
	}{
		{0, 1, 0, 4096, 0}, {4095, 2, 0, 8192, 4095},
		{4096, 4096, 4096, 4096, 0}, {4100, 8192, 4096, 12288, 4},
	} {
		o, n, s, err := directRange(c.start, c.size)
		if err != nil || o != c.offset || n != c.length || s != c.skip {
			t.Fatalf("%+v: %d %d %d %v", c, o, n, s, err)
		}
		b := &directBuffer{}
		aligned := b.aligned(n)
		if uintptr(unsafe.Pointer(&aligned[0]))%4096 != 0 || len(aligned) != n {
			t.Fatal("unaligned buffer")
		}
	}
	for _, c := range []struct {
		start int64
		size  int
	}{{-1, 1}, {0, 0}, {0, -1}, {math.MaxInt64, 1}} {
		if _, _, _, err := directRange(c.start, c.size); err == nil {
			t.Fatal("invalid range accepted")
		}
	}
}

func directFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cached.sst")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	b := SSTableBuilder{File: f, BlockBuffer: new(bytes.Buffer)}
	rng := rand.New(rand.NewSource(20261111))
	keys := rng.Perm(120)[:40]
	sort.Ints(keys)
	for _, k := range keys {
		for seq := uint64(20); seq > 0; seq-- {
			value := make([]byte, 200+rng.Intn(700))
			rng.Read(value)
			if err := b.Add(EncodeInternalKey([]byte(fmt.Sprintf("key%03d", k*2)), seq, seq%7 == 0), value); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Serialize a valid table, then rewrite the bytes without Sync to exercise
	// the direct reader's own barrier for cached fixture writes.
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDirectReadEquivalenceConcurrent(t *testing.T) {
	path := directFixture(t)
	block, err := NewSSTableReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer block.Close()
	direct, err := NewSSTableReaderWithReadPath(path, ReadPathDirect)
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Close()
	straddles := false
	for i := 1; i < len(block.Index); i++ {
		a, _, _ := DecodeInternalKey(block.Index[i-1].Key)
		b, _, _ := DecodeInternalKey(block.Index[i].Key)
		straddles = straddles || bytes.Equal(a, b)
	}
	if !straddles {
		t.Fatal("missing straddled versions")
	}
	stat, _ := block.File.Stat()
	if stat.Size()%4096 == 0 {
		t.Fatal("fixture must have partial EOF sector")
	}
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(worker)))
			for i := 0; i < 120; i++ {
				key := []byte(fmt.Sprintf("key%03d", rng.Intn(244)))
				if i%4 == 0 {
					key, _, _ = DecodeInternalKey(block.Index[rng.Intn(len(block.Index))].Key)
				}
				if i == 0 {
					key = block.maxUserKey
				} // final data block, read beyond EOF
				ts := uint64(rng.Intn(23))
				got, ge := direct.Get(key, ts)
				want, we := block.Get(key, ts)
				if !bytes.Equal(got, want) || !errors.Is(ge, we) {
					t.Errorf("Get %q/%d: %v/%v", key, ts, ge, we)
					return
				}
				gs, ge := direct.LatestValue(key)
				ws, we := block.LatestValue(key)
				if gs != ws || !errors.Is(ge, we) {
					t.Errorf("LatestValue %q: %d/%d %v/%v", key, gs, ws, ge, we)
					return
				}
			}
		}(worker)
	}
	wg.Wait()
}

func TestDirectOpenFailureAndDefault(t *testing.T) {
	path := directFixture(t)
	fault := errors.New("filesystem rejects unbuffered reads")
	calls := 0
	opener := func(string) (*os.File, error) { calls++; return nil, fault }
	for _, mode := range []ReadPathMode{ReadPathBlock, ReadPathLegacyScan} {
		r, err := newSSTableReaderWithOpener(path, mode, opener)
		if err != nil {
			t.Fatal(err)
		}
		if r.directFile != nil || calls != 0 {
			t.Fatal("default/legacy opened direct handle")
		}
		r.Close()
	}
	r, err := newSSTableReaderWithOpener(path, ReadPathDirect, opener)
	if r != nil || !errors.Is(err, fault) || !strings.Contains(err.Error(), "no buffered fallback") || calls != 1 {
		t.Fatalf("%v %v %d", r, err, calls)
	}
	// A direct flag can be accepted at open but rejected on first I/O.
	opener = func(p string) (*os.File, error) {
		f, e := os.Open(p)
		if e == nil {
			f.Close()
		}
		return f, e
	}
	if r, err := newSSTableReaderWithOpener(path, ReadPathDirect, opener); r != nil || err == nil || !strings.Contains(err.Error(), "closed file") {
		t.Fatalf("probe did not fail at reader open: %v", err)
	}
}

func TestDirectManagerValidationAndClose(t *testing.T) {
	for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachAMin, SyncModeApproachB} {
		t.Run(mode.String(), func(t *testing.T) {
			m, err := CreateManagerWithConfig(Config{DataDir: t.TempDir(), Mode: mode, ReadPath: ReadPathDirect, DisableWALSync: true})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			old := m.Begin()
			tx := m.Begin()
			tx.Put("key", "cached fixture")
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			flushReadPathManager(t, m)
			var reader *SSTableReader
			if mode == SyncModeApproachB {
				snap, err := m.AcquireSnapshot()
				if err != nil {
					t.Fatal(err)
				}
				reader = snap.Readers()[0].Reader
				m.ReleaseSnapshot(snap)
			} else {
				reader = m.ActiveSSreaders[0]
			}
			if reader.ReadPath != ReadPathDirect || reader.directFile == nil {
				t.Fatal("manager did not open direct handle")
			}
			reads := 0
			reader.blockReadHook = func() { reads++ }
			old.Put("key", "stale")
			if err := old.Commit(); !errors.Is(err, ErrWriteConflict) {
				t.Fatalf("expected validation conflict: %v", err)
			}
			if reads == 0 {
				t.Fatal("conflict validation did not read SSTable")
			}
			if value, err := m.Get("key", math.MaxUint64); err != nil || value != "cached fixture" {
				t.Fatalf("%q %v", value, err)
			}
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
			if err := reader.directRaw.Control(func(uintptr) { t.Error("closed direct descriptor still usable") }); err == nil {
				t.Fatal("direct descriptor leaked")
			}
		})
	}
}
