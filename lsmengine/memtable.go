package lsmengine

import (
	"bytes"
	"math"
	"math/rand/v2"
)

type Node struct {
	Key     []byte
	Value   []byte
	Forward []*Node
}

type SkipList struct {
	headNode     *Node
	maxLevel     int
	currentLevel int
}

type Memtable struct {
	SK        *SkipList
	SizeBytes int64
}

func (sk *SkipList) randomLevel() int {
	if sk.maxLevel <= 1 {
		return 1
	}
	lvl := 1
	for rand.Float64() < 0.5 && lvl < sk.maxLevel {
		lvl++
	}
	return lvl
}

func (m *Memtable) Put(internalKey []byte, value []byte) {
	curr := m.SK.headNode
	update := make([]*Node, m.SK.maxLevel)
	for level := m.SK.currentLevel - 1; level >= 0; level-- {
		for curr.Forward[level] != nil && CompareInternalKeys(curr.Forward[level].Key, internalKey) < 0 {
			curr = curr.Forward[level]
		}
		update[level] = curr
	}

	newLevel := m.SK.randomLevel()

	if newLevel > m.SK.currentLevel {
		for i := m.SK.currentLevel; i < newLevel; i++ {
			update[i] = m.SK.headNode
		}
		m.SK.currentLevel = newLevel
	}

	newNode := &Node{
		Key:     internalKey,
		Value:   value,
		Forward: make([]*Node, newLevel),
	}

	for i := 0; i < newLevel; i++ {
		newNode.Forward[i] = update[i].Forward[i]
		update[i].Forward[i] = newNode
	}
}

func (m *Memtable) Get(userKey []byte, readTs uint64) ([]byte, error) {
	targetKey := EncodeInternalKey(userKey, readTs, true)

	curr := m.SK.headNode
	for level := m.SK.currentLevel - 1; level >= 0; level-- {
		for curr.Forward[level] != nil && CompareInternalKeys(curr.Forward[level].Key, targetKey) < 0 {
			curr = curr.Forward[level]
		}
	}

	// curr is the node right BEFORE targetKey.
	// The first candidate node >= targetKey is curr.Forward[0].
	candidate := curr.Forward[0]
	if candidate == nil || len(candidate.Key) < 8 {
		return nil, ErrNotFound
	}

	candidateUserKey, candidateSeqNo, isDelete := DecodeInternalKey(candidate.Key)

	// Check if userKey matches and the version is visible to this transaction
	if bytes.Equal(userKey, candidateUserKey) && candidateSeqNo <= readTs {
		if isDelete {
			return nil, ErrKeyDeleted
		}
		return candidate.Value, nil
	}

	return nil, ErrNotFound
}

func (m *Memtable) LatestValue(userKey []byte) (uint64, error) {
	targetKey := EncodeInternalKey(userKey, math.MaxUint64, true)

	curr := m.SK.headNode
	for level := m.SK.currentLevel - 1; level >= 0; level-- {
		for curr.Forward[level] != nil && CompareInternalKeys(curr.Forward[level].Key, targetKey) < 0 {
			curr = curr.Forward[level]
		}
	}

	candidate := curr.Forward[0]
	if candidate == nil || len(candidate.Key) < 8 {
		return 0, ErrNotFound
	}

	candidateUserKey, candidateSeqNo, _ := DecodeInternalKey(candidate.Key)
	if bytes.Equal(userKey, candidateUserKey) {
		return candidateSeqNo, nil
	}

	return 0, ErrNotFound
}
