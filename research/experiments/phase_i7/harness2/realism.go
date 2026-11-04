package harness2

import (
	"errors"
	"fmt"
	"owndbms/lsmengine"
)

func recordUnpacedWriteFailure(sh *Shard, spec Spec, err error) {
	sh.WritesError++
	if spec.IsRealismCell() && errors.Is(err, lsmengine.ErrWriteConflict) {
		sh.WriteConflictAborts++
	}
}

// IsRealismCell confines campaign-only behavior to the eight preregistered cells.
func (s Spec) IsRealismCell() bool {
	if s.Block != "X1" || s.ReadIODelayUs != 0 || (s.ReaderConcurrency != 4 && s.ReaderConcurrency != 16) {
		return false
	}
	if s.ReadPath == "direct" && (s.WriterConcurrency == 1 || s.WriterConcurrency == 2 || s.WriterConcurrency == 4) {
		return s.Cell == fmt.Sprintf("D1_r%02d_w%d_direct", s.ReaderConcurrency, s.WriterConcurrency)
	}
	return s.ReadPath == "block" && s.WriterConcurrency == 1 && s.Cell == fmt.Sprintf("D2_r%02d_w1_buffered", s.ReaderConcurrency)
}
