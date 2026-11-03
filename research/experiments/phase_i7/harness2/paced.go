package harness2

// PacedLoadOutcome exists only for X4. All latency histograms distinguish
// completions inside the window from drain. Closed readers have no response
// schedule, so their response histograms are absent.
type PacedLoadOutcome struct {
	WriteTicksOffered     int64         `json:"write_ticks_offered"`
	WriteTicksUnissued    int64         `json:"write_ticks_unissued"`
	ReadArrivalsAbandoned int64         `json:"read_arrivals_abandoned"`
	MaxReadLatenessNs     int64         `json:"max_read_lateness_ns"`
	WriteService          LatencyStats  `json:"write_service_nanos"`
	WriteResponse         LatencyStats  `json:"write_response_nanos"`
	WriteServiceLate      LatencyStats  `json:"write_service_late_nanos"`
	WriteResponseLate     LatencyStats  `json:"write_response_late_nanos"`
	ReadResponse          *LatencyStats `json:"read_response_nanos,omitempty"`
	ReadResponseLate      *LatencyStats `json:"read_response_late_nanos,omitempty"`
}

type pacedHistograms struct {
	writeService, writeResponse         Histogram
	writeServiceLate, writeResponseLate Histogram
	readResponse, readResponseLate      Histogram
}

func (p *pacedHistograms) recordWrite(issued, intended, done, windowNs int64) {
	if done <= windowNs {
		p.writeService.Record(done - issued)
		p.writeResponse.Record(done - intended)
	} else {
		p.writeServiceLate.Record(done - issued)
		p.writeResponseLate.Record(done - intended)
	}
}

func (s *Shard) observeLateness(ns int64) {
	if ns > s.MaxLatenessNs {
		s.MaxLatenessNs = ns
	}
}

func mergePaced(shards []*Shard, ledger TransactionLedger, openReads bool) *PacedLoadOutcome {
	var p pacedHistograms
	var maxLateness int64
	for _, s := range shards {
		if s.Paced == nil {
			continue
		}
		p.writeService.Merge(&s.Paced.writeService)
		p.writeResponse.Merge(&s.Paced.writeResponse)
		p.writeServiceLate.Merge(&s.Paced.writeServiceLate)
		p.writeResponseLate.Merge(&s.Paced.writeResponseLate)
		p.readResponse.Merge(&s.Paced.readResponse)
		p.readResponseLate.Merge(&s.Paced.readResponseLate)
		if s.MaxLatenessNs > maxLateness {
			maxLateness = s.MaxLatenessNs
		}
	}
	out := &PacedLoadOutcome{
		WriteTicksOffered: ledger.Writes.Offered, WriteTicksUnissued: ledger.Writes.Queued,
		ReadArrivalsAbandoned: ledger.Reads.Abandoned, MaxReadLatenessNs: maxLateness,
		WriteService: p.writeService.Stats(), WriteResponse: p.writeResponse.Stats(),
		WriteServiceLate: p.writeServiceLate.Stats(), WriteResponseLate: p.writeResponseLate.Stats(),
	}
	if openReads {
		r, late := p.readResponse.Stats(), p.readResponseLate.Stats()
		out.ReadResponse, out.ReadResponseLate = &r, &late
	}
	return out
}
