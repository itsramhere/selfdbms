package harness2

import (
	"errors"
	"strings"

	"owndbms/lsmengine"
)

// compactionCounts is owned by the ticker goroutine and read after it joins.
type compactionCounts struct {
	successes, noOps, errors, inWindow uint64
	firstError                         error
}

func (c *compactionCounts) record(err error, didWork, inWindow, stopping bool) {
	if err != nil {
		if stopping && (errors.Is(err, lsmengine.ErrClosed) || strings.Contains(err.Error(), "engine is closed")) {
			return
		}
		c.errors++
		if c.firstError == nil {
			c.firstError = err
		}
	} else if didWork {
		c.successes++
		if inWindow {
			c.inWindow++
		}
	} else {
		c.noOps++
	}
}

func (c *compactionCounts) outcome(res *TrialResult, spec Spec) {
	// Earlier blocks retain their historical trial-failure policy.
	if spec.IsX() && c.firstError != nil {
		res.Status = "FAILED"
		res.Error = "compaction: " + c.firstError.Error()
	}
}
