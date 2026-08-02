package lsmengine

type HeapItem struct {
	InternalKey []byte
	Value       []byte
	IteratorIdx int // Tracks which file iterator this key came from
}

type CompactionHeap []*HeapItem

func (h CompactionHeap) Len() int { return len(h) }

func (h CompactionHeap) Less(i, j int) bool {
	// Reusing your existing comparison logic.
	// Because of how you packed the sequence number, this automatically
	// surfaces the alphabetically smallest key. If keys match, it surfaces
	// the highest sequence number (newest version) first.
	return CompareInternalKeys(h[i].InternalKey, h[j].InternalKey) < 0
}

func (h CompactionHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *CompactionHeap) Push(x any) {
	*h = append(*h, x.(*HeapItem))
}

func (h *CompactionHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[0 : n-1]
	return item
}
