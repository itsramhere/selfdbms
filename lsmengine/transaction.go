package lsmengine

import (
	"errors"
	"fmt"
)

type Transaction struct {
	WriteSet       map[string]PendingWrite
	ReadTs         uint64
	Man            *Manager
	AllocatedSeqNo uint64
}

type PendingWrite struct {
	IsDelete bool
	Data     []byte
}

// gets the value from the WriteSet of the transaction. if not present there, it calls the manager's get function
func (t *Transaction) Get(key string) (string, error) {
	var err error
	val, found := t.WriteSet[key]

	if found {
		if val.IsDelete {
			return "", errors.New("This Key is Deleted.")
		} else {
			return string(val.Data), nil
		}
	}

	value, err := t.Man.Get(key, t.ReadTs)

	if err != nil {
		return " ", err // error message will come upstream from the manager
	}
	return value, nil
}

// adds a new KV Pair to the transaction's writeSet
func (t *Transaction) Put(key string, val string) {

	convertedValue := &PendingWrite{
		IsDelete: false,
		Data:     []byte(val),
	}
	t.WriteSet[key] = *convertedValue
}

func (t *Transaction) Delete(key string) error {
	t.WriteSet[key] = PendingWrite{IsDelete: true, Data: nil}
	return nil
}

func (t *Transaction) Commit() error {
	trace := t.Man.commitTraceObserver
	var phases CommitPhases
	if trace != nil {
		phases.ArrivalNs = t.Man.instrumentClock()
	}
	// Writer arrival sampling (Phase I7 instrumentation): measure the interval
	// from this arrival at Manager.Mu to completed acquisition.
	lockArrival := t.Man.writerLockArrival(LockSiteCommit)
	t.Man.Mu.Lock()
	if trace != nil {
		phases.AcquiredNs = t.Man.instrumentClock()
	}
	if t.Man.commitAcquisitionHook != nil {
		t.Man.commitAcquisitionHook()
	}
	lockArrival.acquired(t.Man)

	if t.Man.state != EngineOpen {
		t.Man.Mu.Unlock()
		t.Abort()
		return ErrClosed
	}

	for key := range t.WriteSet {
		// Use internal unexported latestValueLocked since t.Man.Mu is already locked
		latestSeqNo, err := t.Man.latestValueLocked([]byte(key))
		if err != nil && !errors.Is(err, ErrNotFound) {
			t.Man.Mu.Unlock()
			t.Abort()
			return fmt.Errorf("conflict validation failed for key %q: %w", key, err)
		}

		if err == nil && latestSeqNo > t.ReadTs {
			t.Man.Mu.Unlock()
			t.Abort()
			return errors.New("Write-write conflict detected. Transaction aborted.")
		}
	}

	if trace != nil {
		phases.ValidatedNs = t.Man.instrumentClock()
		phases.WalWriteDoneNs = phases.ValidatedNs
		phases.WalSyncDoneNs = phases.ValidatedNs
	}
	if t.Man.Log == nil || t.Man.Log.File == nil {
		t.Man.Mu.Unlock()
		return errors.New("wal is unavailable")
	}

	t.Man.CurrentSeqNo.Add(1)
	t.AllocatedSeqNo = t.Man.CurrentSeqNo.Load()

	for key, pendingWrite := range t.WriteSet {
		encodedKey := EncodeInternalKey([]byte(key), t.AllocatedSeqNo, pendingWrite.IsDelete)
		var err error
		if trace != nil {
			phases.WalWriteDoneNs, phases.WalSyncDoneNs, err = t.Man.Log.appendTimed(encodedKey, pendingWrite.Data, t.Man.instrumentClock)
		} else {
			err = t.Man.Log.Append(encodedKey, pendingWrite.Data)
		}
		if err != nil {
			t.Man.Mu.Unlock()
			return err
		}

		t.Man.Mem.Put(encodedKey, pendingWrite.Data)

		// Track payload size for the flush threshold
		t.Man.Mem.SizeBytes += int64(len(encodedKey) + len(pendingWrite.Data))
	}

	if trace != nil {
		phases.AppliedNs = t.Man.instrumentClock()
	}
	t.WriteSet = nil

	// Unlock before triggering the flush check to prevent deadlocks
	// with the background worker
	needFlushCheck := !t.Man.inlineFlushCheck || t.Man.memtableAtThresholdLocked()
	t.Man.Mu.Unlock()
	if trace != nil {
		phases.ReleasedNs = t.Man.instrumentClock()
	}
	if needFlushCheck {
		t.Man.checkAndTriggerFlush()
	}
	if trace != nil {
		phases.FlushCheckDoneNs = t.Man.instrumentClock()
		trace.OnCommitPhases(phases)
	}

	return nil
}

func (t *Transaction) Abort() error {
	t.WriteSet = nil
	return nil
}
