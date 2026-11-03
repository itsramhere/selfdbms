"""Follow-up checks on X-settled-v1: compaction tail, fsync bursts by rate, d=2000 p99 shift, sentinel tally, drift."""
import collections, glob, json, math, statistics as st
X = "research/experiments/phase_i7/X_READCOST"
def load(root, cell, r, arm):
    return json.load(open(f"{X}/{root}/trials/{cell}_t{r:02d}_{arm}_a1.json"))["result"]
def hist(n):
    return n.get("stats", n)
print("C1 commit wait p99 (ms)")
for root, cell in (("settled", "R_C1_r16_d0200_on"), ("compaction", "C1_r16_d0200_on")):
    for arm in ("A", "AMIN", "B"):
        c = load(root, cell, 1, arm)["sync_instrumentation"]["writer_lock_wait_nanos"]["commit"]
        if arm == "A" and root == "settled": print("  keys", list(c.keys())[:8])
        v = [hist(load(root, cell, r, arm)["sync_instrumentation"]["writer_lock_wait_nanos"]["commit"])["p99_nanos"] / 1e6 for r in range(1, 11)]
        print(f"  {cell} {arm}: median {st.median(v):.3f} min {min(v):.3f} max {max(v):.3f}")
d = json.load(open(f"{X}/analysis_settled/x_settled_results.json"))
print("sentinel tally", collections.Counter(h["comparison"]["classification"] for h in d["H_C"]), "n", len(d["H_C"]), "triggers", d["rerun_triggers"])
for h in d["H_C"]:
    if h["comparison"]["classification"] != "unchanged":
        print("  ", h["cell"], h["metric"], h["pair"], h["comparison"]["classification"], [round(x, 3) for x in h["comparison"]["t_ci_log"]], "margin", round(h["comparison"]["margin_log"], 3), "orig gm", round(h["original"]["geometric_mean_ratio"], 3), "new gm", round(h["new"]["geometric_mean_ratio"], 3))
print("published-class checks", collections.Counter((h["published_class"], h["new_unadjusted_ci_class"]) for h in d["H_C"] if h["published_class"]))
print("\nfsync bursts by cell (per trial: largest ms median, n>=25ms count mean, trials with any >=25, share of window >=25 mean %)")
by = collections.defaultdict(list)
for tid, t in d["trials"].items():
    if t.get("largest_fsync_ns") is not None:
        by[t["cell"]].append(t)
for c, ts in sorted(by.items()):
    print(f"  {c}: n {len(ts)} W {st.mean(t['W'] for t in ts):.0f} largest {st.median(t['largest_fsync_ns'] for t in ts)/1e6:.1f} count>=25 {st.mean(t['fsync_count_ge_25ms'] for t in ts):.2f} trials_any {sum(t['fsync_count_ge_25ms']>0 for t in ts)} share>=25 {100*st.mean(t['fsync_share_ge_25ms'] for t in ts):.2f}% count>=10 {st.mean(t['fsync_count_ge_10ms'] for t in ts):.1f}")
print("\nby arm, all paced trials")
for arm in ("A", "AMIN", "B"):
    ts = [t for c in by for t in by[c] if t["arm"] == arm]
    print(f"  {arm}: n {len(ts)} any>=25 {sum(t['fsync_count_ge_25ms']>0 for t in ts)} largest median {st.median(t['largest_fsync_ns'] for t in ts)/1e6:.1f}")
print("\nd=2000 write response p99 (ms) by arm, paced vs settled, median and range")
for mode in ("closed", "open"):
    for arm in ("A", "AMIN", "B"):
        for root, cell in (("paced", f"P1_d2000_f20_{mode}"), ("settled", f"S1_d2000_f20_{mode}")):
            v = []; w = []
            for r in range(1, 11):
                x = load(root, cell, r, arm)["paced_load"]["write_response_nanos"]
                v.append(x["p99"] / 1e6); w.append(x["p95"] / 1e6)
            print(f"  {cell} {arm}: p99 median {st.median(v):.2f} range {min(v):.2f}..{max(v):.2f} | p95 median {st.median(w):.2f}")
print("\nwhere slow fsyncs fall, S1_d2000 AMIN+B: wal_sync>=5ms count and time in the window")
pos = []; cnt = []
for mode in ("closed", "open"):
    for arm in ("AMIN", "B"):
        for r in range(1, 11):
            res = load("settled", f"S1_d2000_f20_{mode}", r, arm)
            sd = res["stall_diagnostics"]
            slow = [c for c in sd["commits"] if c["WalSyncDoneNs"] - c["WalWriteDoneNs"] >= 5_000_000]
            cnt.append(len(slow)); pos += [c["ArrivalNs"] / 1e9 for c in slow]
print("  per trial count median", st.median(cnt), "range", min(cnt), max(cnt), "total", len(pos))
print("  by second", collections.Counter(int(p) for p in pos).most_common(12))
print("\nwal_sync p99 settled vs write resp p99, S1_d2000 closed AMIN")
for r in range(1, 11):
    res = load("settled", "S1_d2000_f20_closed", r, "AMIN")
    print("  ", r, round(res["stall_diagnostics"]["phase_histograms"]["wal_sync"]["stats"]["p99"] / 1e6, 2), round(res["paced_load"]["write_response_nanos"]["p99"] / 1e6, 2))
print("\nread drift: slope of log R per round")
print({c: {a: round(v["log_R_slope_per_round"], 4) for a, v in arms.items()} for c, arms in d["round_trend_R"].items() if c.startswith(("S1_d2000", "R_X1_r16_d2000", "R_T1"))})
print("gaps", d["gaps"]["min_ms"], d["gaps"]["median_ms"], d["gaps"]["max_ms"], "low", len(d["gaps"]["first_attempt_below_settle"]))
