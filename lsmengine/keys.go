package lsmengine

import (
	"bytes"
	"encoding/binary"
)

// returns an encoded key used to store KV Pair in WAL & Memtable
func EncodeInternalKey(userKey []byte, seqNo uint64, isDelete bool) []byte {
	// Pack the seqNo and delete flag into a single uint64. Shift seqNo left by 8 bits to leave the lowest byte empty.
	packed := seqNo << 8
	if isDelete {
		// Flip the lowest bit to 1 if it is a tombstone
		packed = packed | 1
	}

	// invert for descending sort
	tempKey := make([]byte, 8)
	binary.BigEndian.PutUint64(tempKey, packed)
	for i := range tempKey {
		tempKey[i] = ^tempKey[i]
	}

	internalKey := make([]byte, 0, len(userKey)+8)
	internalKey = append(internalKey, userKey...)
	internalKey = append(internalKey, tempKey...)

	return internalKey
}

// result is 0 if a == b, -1 if a < b, and +1 if a > b
func CompareInternalKeys(a, b []byte) int {
	return bytes.Compare(a, b)
}

// returns user-key, seqNo of key & isDelete values
func DecodeInternalKey(internalKey []byte) (userKey []byte, seqNo uint64, isDelete bool) {
	n := len(internalKey)
	userKey = internalKey[:n-8]

	tempKey := make([]byte, 8)
	copy(tempKey, internalKey[n-8:])

	for i := range tempKey {
		tempKey[i] = ^tempKey[i]
	}

	packed := binary.BigEndian.Uint64(tempKey)

	isDelete = (packed & 1) == 1
	seqNo = packed >> 8

	return userKey, seqNo, isDelete
}
