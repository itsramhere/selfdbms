"""Report diagnostic commit stalls. Uses only stalldiag outputs, never campaign evidence."""

import argparse
from collections import Counter, defaultdict
import json
from pathlib import Path
import statistics
import sys


BOUNDARIES = (
    "ArrivalNs", "AcquiredNs", "ValidatedNs", "WalWriteDoneNs",
    "WalSyncDoneNs", "AppliedNs", "ReleasedNs", "FlushCheckDoneNs",
)
PHASES = ("lock_wait", "validate", "wal_write", "wal_sync", "apply", "release", "flush_check")


def phases(commit):
    return {name: commit[end] - commit[start]
            for name, start, end in zip(PHASES, BOUNDARIES, BOUNDARIES[1:])}


def total(commit):
    return commit["FlushCheckDoneNs"] - commit["ArrivalNs"]


def dominant(commit):
    durations = phases(commit)
    name = max(durations, key=durations.get)
    return name, durations[name] / total(commit) if total(commit) else 0


def overlaps(start, length, gap):
    return start < gap["start_ns"] + gap["length_ns"] and gap["start_ns"] < start + length


def load_outputs(directory):
    outputs = []
    for path in sorted(Path(directory).glob("*.json")):
        with path.open(encoding="utf-8-sig") as handle:
            record = json.load(handle)
        if record.get("diagnostic") is not True:
            raise ValueError(f"{path}: missing diagnostic=true")
        result = record.get("result", record)
        if not result.get("stall_diagnostics"):
            raise ValueError(f"{path}: missing stall_diagnostics")
        outputs.append((path.name, result))
    return outputs


def report(outputs, stream=sys.stdout):
    def emit(message=""):
        print(message, file=stream)

    groups = defaultdict(list)
    for filename, result in outputs:
        spec, diag = result["config"], result["stall_diagnostics"]
        key = (result["arm"], result["cell"], spec["read_io_delay_us"],
               spec["paced_write_rate_per_sec"], spec["wal_sync"])
        commits = sorted(diag["commits"], key=total, reverse=True)
        reads = diag.get("slowest_reads", [])
        slow_read = max(reads, key=lambda r: r["length_ns"], default=None)
        gaps = diag.get("heartbeat_gaps", [])
        pauses = diag.get("runtime", {}).get("gc_pauses", [])
        groups[key].append(commits)
        emit(f"{filename}: arm={key[0]} cell={key[1]} delay_us={key[2]} write_rate={key[3]} wal_sync={key[4]}")
        emit(f"  commits={diag.get('commit_count', '?')} retained={len(commits)} dropped={diag.get('commits_dropped', 0)}")
        emit("  10 slowest commits (ms; time is arrival offset in window):")
        for commit in commits[:10]:
            name, share = dominant(commit)
            durations = " ".join(f"{phase}={ns / 1e6:.3f}" for phase, ns in phases(commit).items())
            emit(f"    index={commit['index']} time={commit['ArrivalNs'] / 1e6:.3f} total={total(commit) / 1e6:.3f} {durations} largest={name} ({share:.1%})")
        if slow_read:
            emit(f"  slowest read: worker={slow_read['worker']} start_ms={slow_read['start_ns'] / 1e6:.3f} length_ms={slow_read['length_ns'] / 1e6:.3f}")
        else:
            emit("  slowest read: none")
        large_gaps = [g for g in gaps if g["length_ns"] >= 10_000_000]
        emit(f"  heartbeat gaps >=10ms: {len(large_gaps)} (dropped={diag.get('heartbeat_dropped', 0)})")
        for gap in large_gaps:
            emit(f"    start_ms={gap['start_ns'] / 1e6:.3f} length_ms={gap['length_ns'] / 1e6:.3f}")
        emit(f"  GC pauses ending in window: {len(pauses)} (history_lost={diag.get('runtime', {}).get('gc_history_lost', False)})")
        for pause in pauses:
            emit(f"    end_ms={(pause['start_ns'] + pause['length_ns']) / 1e6:.3f} length_ms={pause['length_ns'] / 1e6:.3f}")
        if commits:
            largest = commits[0]
            start, length = largest["ArrivalNs"], total(largest)
            emit("  slowest commit overlaps: "
                 f"heartbeat={any(overlaps(start, length, g) for g in gaps)} "
                 f"GC={any(overlaps(start, length, p) for p in pauses)} "
                 f"slowest_read={bool(slow_read and overlaps(start, length, slow_read))}")
        emit()

    emit("Summary by arm and cell (slow means total >=10ms; phase shares use retained commits):")
    largest_all = [commits[0] for trials in groups.values() for commits in trials if commits]
    emit(f"  Across outputs: largest_is_first={sum(c['index'] == 1 for c in largest_all)}/{len(largest_all)}")
    for key, trials in sorted(groups.items()):
        largest = [commits[0] for commits in trials if commits]
        slow = [c for commits in trials for c in commits if total(c) >= 10_000_000]
        median = statistics.median(total(c) for c in largest) / 1e6 if largest else 0
        first = sum(c["index"] == 1 for c in largest)
        emit(f"  arm={key[0]} cell={key[1]} delay_us={key[2]} write_rate={key[3]} wal_sync={key[4]} median_largest_ms={median:.3f} largest_is_first={first}/{len(largest)}")
        counts = Counter(dominant(c)[0] for c in slow)
        phase_time = Counter()
        for commit in slow:
            phase_time.update(phases(commit))
        all_time = sum(phase_time.values())
        emit(f"    slow commits={len(slow)}; phase: dominant count/share, elapsed share")
        for phase in PHASES:
            share = counts[phase] / len(slow) if slow else 0
            time_share = phase_time[phase] / all_time if all_time else 0
            emit(f"      {phase}: {counts[phase]}/{len(slow)} ({share:.1%}), time={time_share:.1%}")
        bins = Counter(c["ArrivalNs"] // 1_000_000_000 for c in slow)
        emit("    slow commit positions (1s bins): " + (", ".join(
            f"[{i},{i+1})s={bins[i]}" for i in sorted(bins)) or "none"))


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    args = parser.parse_args(argv)
    try:
        outputs = load_outputs(args.directory)
        if not outputs:
            parser.error("no diagnostic JSON outputs found")
        report(outputs)
    except (OSError, ValueError, KeyError) as error:
        parser.error(str(error))


if __name__ == "__main__":
    main()
