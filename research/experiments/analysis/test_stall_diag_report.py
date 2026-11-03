import io
import json
from pathlib import Path
import shutil
import sys
import unittest
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parent))
import stall_diag_report as report


FIXTURE = r'''{
  "diagnostic": true, "arm": "A", "cell": "paced",
  "config": {"read_io_delay_us": 2000, "paced_write_rate_per_sec": 72, "wal_sync": true},
  "stall_diagnostics": {
    "commit_count": 2, "commits_dropped": 0,
    "commits": [
      {"index": 1, "ArrivalNs": 1000000000, "AcquiredNs": 1000100000,
       "ValidatedNs": 1000200000, "WalWriteDoneNs": 1000300000,
       "WalSyncDoneNs": 1050300000, "AppliedNs": 1050400000,
       "ReleasedNs": 1050500000, "FlushCheckDoneNs": 1051000000},
      {"index": 2, "ArrivalNs": 2000000000, "AcquiredNs": 2000100000,
       "ValidatedNs": 2000200000, "WalWriteDoneNs": 2000300000,
       "WalSyncDoneNs": 2000400000, "AppliedNs": 2000500000,
       "ReleasedNs": 2000600000, "FlushCheckDoneNs": 2001000000}
    ],
    "slowest_reads": [{"worker": 0, "start_ns": 1005000000, "length_ns": 55000000}],
    "heartbeat_gaps": [{"start_ns": 1010000000, "length_ns": 20000000}],
    "runtime": {"gc_pauses": [{"start_ns": 1020000000, "length_ns": 1000000}]}
  }
}'''


class StallDiagReportTest(unittest.TestCase):
    def setUp(self):
        # Inherit workspace permissions; Windows Python 3.13 mode=700 excludes the sandbox.
        self.root = Path(__file__).resolve().parent / (".stall_diag_test_" + uuid.uuid4().hex)
        self.root.mkdir()
        self.addCleanup(shutil.rmtree, self.root)

    def test_fixture_report(self):
        (self.root / "a.json").write_text(FIXTURE, encoding="utf-8")
        outputs = report.load_outputs(self.root)
        out = io.StringIO()
        report.report(outputs, out)
        text = out.getvalue()
        for expected in (
            "delay_us=2000 write_rate=72 wal_sync=True", "index=1 time=1000.000 total=51.000",
            "wal_sync=50.000", "largest=wal_sync (98.0%)", "start_ms=1005.000 length_ms=55.000",
            "heartbeat gaps >=10ms: 1", "end_ms=1021.000 length_ms=1.000",
            "heartbeat=True GC=True slowest_read=True", "median_largest_ms=51.000",
            "largest_is_first=1/1", "wal_sync: 1/1 (100.0%)", "[1,2)s=1",
        ):
            self.assertIn(expected, text)

    def test_overlap_boundaries(self):
        gap = {"start_ns": 10, "length_ns": 5}
        self.assertFalse(report.overlaps(0, 10, gap))
        self.assertFalse(report.overlaps(15, 10, gap))
        self.assertTrue(report.overlaps(14, 1, gap))

    def test_refuses_campaign_record(self):
        record = json.loads(FIXTURE)
        del record["diagnostic"]
        (self.root / "a.json").write_text(json.dumps(record), encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "diagnostic=true"):
            report.load_outputs(self.root)


if __name__ == "__main__":
    unittest.main()
