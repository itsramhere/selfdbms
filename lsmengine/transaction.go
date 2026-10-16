package lsmengine

import (
	"errors"
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
	t.Man.Mu.Lock()

	if t.Man.state != EngineOpen {
		t.Man.Mu.Unlock()
		t.Abort()
		return ErrClosed
	}

	for key := range t.WriteSet {
		// Use internal unexported latestValueLocked since t.Man.Mu is already locked
		latestSeqNo, err := t.Man.latestValueLocked([]byte(key))

		if err == nil && latestSeqNo > t.ReadTs {
			t.Man.Mu.Unlock()
			t.Abort()
			return errors.New("Write-write conflict detected. Transaction aborted.")
		}
	}

	if t.Man.Log == nil || t.Man.Log.File == nil {
		t.Man.Mu.Unlock()
		return errors.New("wal is unavailable")
	}

	t.Man.CurrentSeqNo.Add(1)
	t.AllocatedSeqNo = t.Man.CurrentSeqNo.Load()

	for key, pendingWrite := range t.WriteSet {
		encodedKey := EncodeInternalKey([]byte(key), t.AllocatedSeqNo, pendingWrite.IsDelete)
		err := t.Man.Log.Append(encodedKey, pendingWrite.Data)
		if err != nil {
			t.Man.Mu.Unlock()
			return err
		}

		t.Man.Mem.Put(encodedKey, pendingWrite.Data)

		// Track payload size for the flush threshold
		t.Man.Mem.SizeBytes += int64(len(encodedKey) + len(pendingWrite.Data))
	}

	t.WriteSet = nil

	// Unlock before triggering the flush check to prevent deadlocks
	// with the background worker
	t.Man.Mu.Unlock()

	t.Man.checkAndTriggerFlush()

	return nil
}

func (t *Transaction) Abort() error {
	t.WriteSet = nil
	return nil
}
