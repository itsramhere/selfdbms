package lsmengine

// Approach A-min (Phase I7 G1) correctness suite.
//
// Design and proof obligations: research/experiments/phase_i7/01_A_MIN_DESIGN.md
// Test IDs T-01..T-14 map to that document's §9.
//
// Every test runs on the native host filesystem. On Windows the unlink-after-close
// ordering is load-bearing: a reader that holds a lease must still be able to read
// through its descriptor while the table has been retired.

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func aminKey(idx int) string { return fmt.Sprintf("user_key_%010d", idx) }

func aminVal(idx, n int) string {
	prefix := fmt.Sprintf("val_%010d_", idx)
	for len(prefix) < n {
		prefix += "x"
	}
	return prefix[:n]
}

// aminFixture builds `tables` SSTables of `keys` keys each, then leaves the engine
// with an empty active Memtable and a 64 MiB flush threshold.
func aminFixture(t *testing.T, mode SyncMode, tables, keys int) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	man, err := CreateManagerWithConfig(Config{
		Mode:                   mode,
		WalPath:                filepath.Join(dir, "wal.log"),
		DataDir:                dir,
		MemtableThresholdBytes: 64 << 20,
	})
	if err != nil {
		t.Fatalf("CreateManagerWithConfig(%v): %v", mode, err)
	}
	for tn := 0; tn < tables; tn++ {
		tx := man.Begin()
		if tx == nil {
			t.Fatalf("Begin returned nil")
		}
		for k := 0; k < keys; k++ {
			idx := tn*keys + k
			tx.Put(aminKey(idx), aminVal(idx, 128))
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("fixture commit: %v", err)
		}
		if err := man.TriggerFlush(); err != nil {
			t.Fatalf("fixture trigger flush: %v", err)
		}
		if err := man.WaitForFlushCompletion(); err != nil {
			t.Fatalf("fixture flush: %v", err)
		}
	}
	if got := man.ActiveSSTableCount(); got != tables {
		t.Fatalf("fixture expected %d tables, got %d", tables, got)
	}
	return man, dir
}

// T-13: observational equivalence with Approach A across the full operation set.
func TestAMin_ParityWithApproachA(t *testing.T) {
	type op struct {
		kind  string // put | del | get | getAt
		key   int
		val   int
		readT uint64
	}
	seq := make([]op, 0, 400)
	for i := 0; i < 120; i++ {
		seq = append(seq, op{kind: "put", key: i % 40, val: i})
	}
	for i := 0; i < 40; i++ {
		seq = append(seq, op{kind: "get", key: i})
	}
	for i := 0; i < 20; i += 2 {
		seq = append(seq, op{kind: "del", key: i})
	}
	for i := 0; i < 40; i++ {
		seq = append(seq, op{kind: "get", key: i})
	}
	seq = append(seq, op{kind: "get", key: 9999}) // absent
	seq = append(seq, op{kind: "put", key: 3, val: 500})

	run := func(mode SyncMode) []string {
		man, _ := aminFixture(t, mode, 2, 40)
		defer man.Close()
		out := make([]string, 0, len(seq))
		for _, o := range seq {
			switch o.kind {
			case "put":
				tx := man.Begin()
				tx.Put(aminKey(o.key), aminVal(o.val, 128))
				if err := tx.Commit(); err != nil {
					out = append(out, "commit_err:"+err.Error())
					continue
				}
				out = append(out, "ok")
			case "del":
				tx := man.Begin()
				if err := tx.Delete(aminKey(o.key)); err != nil {
					out = append(out, "del_err:"+err.Error())
					continue
				}
				if err := tx.Commit(); err != nil {
					out = append(out, "commit_err:"+err.Error())
					continue
				}
				out = append(out, "ok")
			case "get":
				v, err := man.Get(aminKey(o.key), math.MaxUint64)
				out = append(out, fmt.Sprintf("%q|%v", v, err))
			}
		}
		return out
	}

	a := run(SyncModeApproachA)
	amin := run(SyncModeApproachAMin)
	if len(a) != len(amin) {
		t.Fatalf("result length mismatch: A=%d AMIN=%d", len(a), len(amin))
	}
	for i := range a {
		if a[i] != amin[i] {
			t.Fatalf("op %d divergence:\n A    = %s\n AMIN = %s", i, a[i], amin[i])
		}
	}
}

// T-02/T-09: every lease taken is released on every return path.
func TestAMin_LeasesAreBalanced(t *testing.T) {
	man, _ := aminFixture(t, SyncModeApproachAMin, 3, 30)
	defer man.Close()

	tx := man.Begin()
	tx.Put("tombstone_key", "dead")
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := man.WaitForFlushCompletion(); err != nil {
		t.Fatalf("wait: %v", err)
	}

	probes := []string{aminKey(0), aminKey(45), aminKey(80), "absent_key", "tombstone_key"}
	for _, p := range probes {
		_, _ = man.Get(p, math.MaxUint64)
	}
	man.Mu.RLock()
	for _, r := range man.ActiveSSreaders {
		if n := r.leaseCount.Load(); n != 0 {
			man.Mu.RUnlock()
			t.Fatalf("lease leaked: %s has %d outstanding leases", r.File.Name(), n)
		}
	}
	man.Mu.RUnlock()
}

// T-03/T-07: a table retired during an in-flight scan stays readable until the
// last lease is released, and is unlinked only afterwards (NTFS ordering).
func TestAMin_CompactionDuringScan(t *testing.T) {
	man, _ := aminFixture(t, SyncModeApproachAMin, 4, 30)
	defer man.Close()

	man.Mu.RLock()
	before := make([]string, len(man.ActiveSSreaders))
	for i, r := range man.ActiveSSreaders {
		before[i] = r.File.Name()
	}
	oldest := man.ActiveSSreaders[0]
	man.Mu.RUnlock()

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	man.duringPointReadHook = func() {
		once.Do(func() {
			close(entered)
			<-release
		})
	}

	type res struct {
		v   string
		err error
	}
	done := make(chan res, 1)
	go func() {
		v, err := man.Get(aminKey(0), math.MaxUint64) // lives in the oldest table
		done <- res{v, err}
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("reader never reached the paused point")
	}

	// The reader holds leases on all four tables and no manager lock.
	if err := man.CompactAll(); err != nil {
		t.Fatalf("compaction during scan: %v", err)
	}

	// Retired but leased: descriptor must still work, files must still exist.
	if _, err := oldest.File.Stat(); err != nil {
		t.Fatalf("retired table descriptor invalid while leased: %v", err)
	}
	for _, f := range before {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("retired table unlinked while leased: %s: %v", f, err)
		}
	}
	if n := oldest.leaseCount.Load(); n < 1 {
		t.Fatalf("expected an outstanding lease during the scan, got %d", n)
	}

	close(release)
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("read failed after concurrent compaction: %v", r.err)
		}
		if r.v != aminVal(0, 128) {
			t.Fatalf("read returned wrong value after concurrent compaction")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read did not complete")
	}

	// After the last lease is released, retirement must complete: closed + unlinked.
	deadline := time.Now().Add(3 * time.Second)
	for {
		remaining := 0
		for _, f := range before {
			if _, err := os.Stat(f); err == nil {
				remaining++
			}
		}
		if remaining == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d superseded tables still present after lease release", remaining)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := oldest.leaseCount.Load(); n != 0 {
		t.Fatalf("lease not balanced after scan: %d", n)
	}
}

// T-11/T-12: Close blocks on an outstanding lease and completes once it drains.
func TestAMin_ShutdownDrainsLeases(t *testing.T) {
	man, _ := aminFixture(t, SyncModeApproachAMin, 2, 20)

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	man.duringPointReadHook = func() {
		once.Do(func() {
			close(entered)
			<-release
		})
	}

	readDone := make(chan error, 1)
	go func() {
		_, err := man.Get(aminKey(0), math.MaxUint64)
		readDone <- err
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("reader never paused")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- man.Close() }()

	select {
	case err := <-closeDone:
		t.Fatalf("Close returned while a lease was outstanding (err=%v)", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(release)
	if err := <-readDone; err != nil {
		t.Fatalf("in-flight read failed during shutdown: %v", err)
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close returned error after drain: %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("Close did not complete after leases drained")
	}
}

// T-08: churn (flush + compaction + writers + readers) leaves no orphan tables.
func TestAMin_NoOrphanTablesUnderChurn(t *testing.T) {
	dir := t.TempDir()
	man, err := CreateManagerWithConfig(Config{
		Mode:                   SyncModeApproachAMin,
		WalPath:                filepath.Join(dir, "wal.log"),
		DataDir:                dir,
		MemtableThresholdBytes: 16 * 1024,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Seed a few tables.
	for tn := 0; tn < 2; tn++ {
		tx := man.Begin()
		for k := 0; k < 80; k++ {
			idx := tn*80 + k
			tx.Put(aminKey(idx), aminVal(idx, 128))
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("seed commit: %v", err)
		}
		if err := man.TriggerFlush(); err != nil {
			t.Fatalf("seed flush: %v", err)
		}
		if err := man.WaitForFlushCompletion(); err != nil {
			t.Fatalf("seed wait: %v", err)
		}
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			i := r
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = man.Get(aminKey(i%160), math.MaxUint64)
					i++
				}
			}
		}(r)
	}
	var writes atomic.Int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
				idx := i % 160
				tx := man.Begin()
				tx.Put(aminKey(idx), aminVal(idx, 128))
				if err := tx.Commit(); err == nil {
					writes.Add(1)
				}
				i++
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		tk := time.NewTicker(40 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				_ = man.CompactAll()
			}
		}
	}()

	time.Sleep(1200 * time.Millisecond)
	close(stop)
	wg.Wait()

	if err := man.WaitForFlushCompletion(); err != nil {
		t.Fatalf("drain flushes: %v", err)
	}
	if writes.Load() == 0 {
		t.Fatal("writer made no progress; churn test is not exercising anything")
	}
	if err := man.CompactAll(); err != nil {
		t.Fatalf("final compaction: %v", err)
	}
	if err := man.drainReaderLeases(); err != nil {
		t.Fatalf("lease drain: %v", err)
	}

	man.Mu.RLock()
	published := len(man.ActiveSSreaders)
	man.Mu.RUnlock()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	sst := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".sst" {
			sst++
		}
	}
	if sst != published {
		t.Fatalf("orphan tables: %d .sst files on disk, %d published readers", sst, published)
	}
	if err := man.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// T-10/M-7: A-min must not add steady-state allocations to the disk read path.
func TestAMin_NoExtraAllocations(t *testing.T) {
	measure := func(mode SyncMode) float64 {
		man, _ := aminFixture(t, mode, 2, 25)
		defer man.Close()
		// Warm the scratch pool / caches.
		for i := 0; i < 20; i++ {
			_, _ = man.Get(aminKey(0), math.MaxUint64)
		}
		return testing.AllocsPerRun(200, func() {
			_, _ = man.Get(aminKey(0), math.MaxUint64)
		})
	}
	a := measure(SyncModeApproachA)
	amin := measure(SyncModeApproachAMin)
	if amin > a+1.0 {
		t.Fatalf("A-min added allocations: A=%.2f AMIN=%.2f allocs/read", a, amin)
	}
	t.Logf("allocs/read: A=%.2f AMIN=%.2f", a, amin)
}

// Concurrent Close with live readers must not deadlock or panic.
func TestAMin_ConcurrentCloseWithReaders(t *testing.T) {
	man, _ := aminFixture(t, SyncModeApproachAMin, 2, 30)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for r := 0; r < 6; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			i := r
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = man.Get(aminKey(i%60), math.MaxUint64)
					i++
				}
			}
		}(r)
	}
	time.Sleep(80 * time.Millisecond)
	closed := make(chan error, 1)
	go func() { closed <- man.Close() }()
	select {
	case err := <-closed:
		if err != nil && !errors.Is(err, ErrClosed) {
			t.Fatalf("Close error: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("Close deadlocked with live readers")
	}
	close(stop)
	wg.Wait()
}

// T-15: shutdown must drain leases on RETIRED tables, not only on active ones.
// Combines retirement + an outstanding lease + a concurrent Close in one
// deterministic interleaving.
func TestAMin_ShutdownWaitsForRetiredLease(t *testing.T) {
	man, _ := aminFixture(t, SyncModeApproachAMin, 4, 30)

	man.Mu.RLock()
	before := make([]string, len(man.ActiveSSreaders))
	for i, r := range man.ActiveSSreaders {
		before[i] = r.File.Name()
	}
	man.Mu.RUnlock()

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	man.duringPointReadHook = func() {
		once.Do(func() {
			close(entered)
			<-release
		})
	}

	readDone := make(chan error, 1)
	go func() {
		_, err := man.Get(aminKey(0), math.MaxUint64)
		readDone <- err
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("reader never reached the paused point")
	}

	// Retire all four inputs while the reader holds a lease on each.
	if err := man.CompactAll(); err != nil {
		t.Fatalf("compaction during scan: %v", err)
	}
	man.Mu.RLock()
	retiredCount := len(man.retiredReaders)
	man.Mu.RUnlock()
	if retiredCount != len(before) {
		t.Fatalf("expected %d retired readers tracked for shutdown, got %d", len(before), retiredCount)
	}

	// Close must not return while a retired table is leased.
	closeDone := make(chan error, 1)
	go func() { closeDone <- man.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned while a RETIRED table held a lease (err=%v)", err)
	case <-time.After(250 * time.Millisecond):
	}

	// ...and must not have closed or unlinked anything the reader still needs.
	for _, f := range before {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("retired table removed before its lease drained: %s: %v", f, err)
		}
	}

	close(release)
	if err := <-readDone; err != nil {
		t.Fatalf("in-flight read failed: %v", err)
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close after drain: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("Close did not complete after the retired lease drained")
	}

	// Reclamation finished inside shutdown: every retired file is gone.
	for _, f := range before {
		if _, err := os.Stat(f); err == nil {
			t.Fatalf("retired table not reclaimed by shutdown: %s", f)
		}
	}
}

// T-16: the drain timeout must fail CLOSED. A reader that never releases must
// leave Close returning an error while the descriptor stays open and the file
// stays on disk; releasing afterwards reclaims exactly once.
func TestAMin_ShutdownTimeoutFailsClosed(t *testing.T) {
	man, _ := aminFixture(t, SyncModeApproachAMin, 2, 20)
	man.leaseDrainTimeout = 50 * time.Millisecond

	man.Mu.RLock()
	held := make([]*SSTableReader, 0, len(man.ActiveSSreaders))
	files := make([]string, 0, len(man.ActiveSSreaders))
	for _, r := range man.ActiveSSreaders {
		held = append(held, r)
		files = append(files, r.File.Name())
		r.lease() // simulate a reader that is still scanning
	}
	man.Mu.RUnlock()

	err := man.Close()
	if err == nil {
		t.Fatal("Close returned nil with an outstanding lease; it must fail closed")
	}
	if !strings.Contains(err.Error(), "lease") {
		t.Fatalf("Close error does not mention the lease: %v", err)
	}

	// Nothing may be closed or unlinked while the lease is outstanding.
	for i, r := range held {
		if _, statErr := r.File.Stat(); statErr != nil {
			t.Fatalf("descriptor %d was closed despite an outstanding lease: %v", i, statErr)
		}
		if _, statErr := os.Stat(files[i]); statErr != nil {
			t.Fatalf("file %d was unlinked despite an outstanding lease: %v", i, statErr)
		}
	}

	// The "reader" finishes and releases. These are ACTIVE (never retired) tables,
	// so a release only decrements the lease; the descriptor is left for Close's
	// caller to inspect. That is the fail-closed state.
	for _, r := range held {
		if r.releaseLease() {
			t.Fatal("an active table must not be reclaimed by a lease release")
		}
	}
	for i, r := range held {
		if _, statErr := os.Stat(files[i]); statErr != nil {
			t.Fatalf("active file %d disappeared: %v", i, statErr)
		}
		if err := r.Close(); err != nil {
			t.Fatalf("late close of active table %d: %v", i, err)
		}
	}
}
