"""Exact sizes of the per-trial maxima, and how readers behave during long holds.

A process-wide freeze would catch readers inside their critical section. At
d=2000 CoarseLock readers spend most of their time inside it, so a freeze of
55 ms would push the critical-section maximum to about 55 ms.
"""
import json, glob, os, re, collections, statistics as st
root = r"C:\Users\ramst\Documents\Projects\selfdbms\research\experiments\phase_i7\X_READCOST\paced\trials"
vals, a2000 = [], []
for f in sorted(glob.glob(os.path.join(root, "*_a1.json"))):
    d = json.load(open(f))
    r = d["result"]
    p = r["paced_load"]
    wmax = max(p["write_service_nanos"]["max"], p["write_service_late_nanos"]["max"]) / 1e6
    vals.append(wmax)
    if "_d2000_" in f and "_A_a1" in f:
        cs = next(iter(r["sync_instrumentation"]["read_critical_section_nanos"].values()))
        a2000.append((cs["max_nanos"] / 1e6, cs["mean_nanos"] / 1e6, cs["count"], wmax))
vals.sort()
print("n", len(vals), "min", vals[0], "median", st.median(vals), "max", vals[-1])
hist = collections.Counter(int(v // 5) * 5 for v in vals)
print("5 ms bins of per-trial write max:")
for k in sorted(hist):
    print(f"  {k:>4}-{k+5:<4} {hist[k]:>4} {'#' * hist[k]}")
print("\nDistance to nearest multiple of 15.625 ms, ms:")
dist = [min(v % 15.625, 15.625 - v % 15.625) for v in vals]
print("  median", round(st.median(dist), 2), "(uniform would give about 3.9)")
print("\nCoarseLock, d=2000: read critical section vs write max")
print("  median cs max ms", round(st.median(x[0] for x in a2000), 2), " largest cs max ms", round(max(x[0] for x in a2000), 2),
      " mean cs ms", round(st.median(x[1] for x in a2000), 2), " median write max ms", round(st.median(x[3] for x in a2000), 2))
print("  trials", len(a2000))
