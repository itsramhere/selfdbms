"""My own attribution from the raw diagnostic JSON, independent of the report tool."""
import json, glob, os, collections, statistics as st
root = r"C:\Users\ramst\Documents\Projects\selfdbms\research\experiments\phase_i7\X_READCOST\stall_diag\runs"
PH = ["lock", "validate", "write", "sync", "apply", "release", "flushchk"]
def phases(c):
    return dict(lock=c["ArrivalNs"] and c["AcquiredNs"] - c["ArrivalNs"], validate=c["ValidatedNs"] - c["AcquiredNs"], write=c["WalWriteDoneNs"] - c["ValidatedNs"],
                sync=c["WalSyncDoneNs"] - c["WalWriteDoneNs"], apply=c["AppliedNs"] - c["WalSyncDoneNs"], release=c["ReleasedNs"] - c["AppliedNs"],
                flushchk=c["FlushCheckDoneNs"] - c["ReleasedNs"])
rows = []
print(f"{'trial':<44}{'wmax':>7}{'trc':>7}{'idx':>6}{'at_s':>7} " + " ".join(f"{p:>7}" for p in PH) + f"{'rmax':>7}{'hbgap':>7}{'gcmax':>7}{'n>=10':>6}{'n>=25':>6}")
for f in sorted(glob.glob(os.path.join(root, "*.json"))):
    d = json.load(open(f))
    s = d["stall_diagnostics"]
    p = d["paced_load"]
    wmax = max(p["write_service_nanos"]["max"], p["write_service_late_nanos"]["max"]) / 1e6
    tot = lambda c: c["FlushCheckDoneNs"] - c["ArrivalNs"]
    cs = s["commits"]
    top = max(cs, key=tot)
    ph = phases(top)
    n10 = sum(tot(c) >= 10e6 for c in cs)
    n25 = sum(tot(c) >= 25e6 for c in cs)
    gaps = s["heartbeat_gaps"] or []
    # heartbeat gap overlapping the largest commit
    a, b = top["ArrivalNs"], top["FlushCheckDoneNs"]
    ov = [g["length_ns"] for g in gaps if g["start_ns"] < b and g["start_ns"] + g["length_ns"] > a]
    gc = [g["length_ns"] for g in s["runtime"]["gc_pauses"]]
    lr = d["latency_read_nanos"]["max"] / 1e6
    print(f"{d['trial_id']:<44}{wmax:>7.1f}{tot(top)/1e6:>7.1f}{top['index']:>6}{a/1e9:>7.2f} " + " ".join(f"{ph[k]/1e6:>7.2f}" for k in PH) +
          f"{lr:>7.1f}{(max(ov)/1e6 if ov else 0):>7.1f}{(max(gc)/1e6 if gc else 0):>7.2f}{n10:>6}{n25:>6}")
    rows.append(dict(id=d["trial_id"], wmax=wmax, trc=tot(top) / 1e6, idx=top["index"], at=a / 1e9, ph=ph, sync=d["config"]["wal_sync"], cs=cs, gaps=gaps, gc=gc, tot=tot))
print()
for sync in (True, False):
    v = [r for r in rows if r["sync"] == sync]
    print(f"wal_sync={sync}: trials {len(v)} median write max {st.median(r['wmax'] for r in v):.1f}, median traced max {st.median(r['trc'] for r in v):.1f}, largest {max(r['wmax'] for r in v):.1f}")
    print("  largest commit, phase holding the most time:", collections.Counter(max(r["ph"], key=r["ph"].get) for r in v))
    print("  largest commit is the first commit:", sum(r["idx"] == 1 for r in v))
    bins = collections.Counter(int(r["at"]) for r in v)
    print("  second of window of largest commit:", dict(sorted(bins.items())))
    all_slow = [(c, r["tot"](c)) for r in v for c in r["cs"] if r["tot"](c) >= 10e6]
    print("  commits >=10 ms:", len(all_slow), "by phase:", collections.Counter(max(phases(c), key=phases(c).get) for c, _ in all_slow))
    print("  first commits' total ms, sorted:", sorted(round(r["tot"](c) / 1e6, 1) for r in v for c in r["cs"] if c["index"] == 1))
    print("  second commits' total ms, sorted:", sorted(round(r["tot"](c) / 1e6, 1) for r in v for c in r["cs"] if c["index"] == 2))
