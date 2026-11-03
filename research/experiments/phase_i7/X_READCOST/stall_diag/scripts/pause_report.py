"""Largest fsync by pause level in the calibration."""
import json, glob, os, re, collections, statistics as st
D = r"C:\Users\ramst\Documents\Projects\selfdbms\research\experiments\phase_i7\X_READCOST\stall_diag\pause"
g = collections.defaultdict(list)
for f in sorted(glob.glob(os.path.join(D, "C_*.json"))):
    d = json.load(open(f))
    m = re.search(r"_p(\d+)$", d["trial_id"])
    sy = [((c["WalSyncDoneNs"] - c["WalWriteDoneNs"]) / 1e6, c["ArrivalNs"] / 1e9) for c in d["stall_diagnostics"]["commits"]]
    top = max(sy)
    g[int(m[1])].append((top[0], top[1], sum(x >= 25 for x, _ in sy)))
print(f"{'pause':>6}{'n':>3}{'median':>8}{'min':>7}{'max':>7}  values (ms @ s; count >=25 ms)")
for p in sorted(g):
    v = g[p]
    xs = [x[0] for x in v]
    print(f"{p:>6}{len(v):>3}{st.median(xs):>8.1f}{min(xs):>7.1f}{max(xs):>7.1f}  " + " ".join(f"{a:.0f}@{b:.1f}({c})" for a, b, c in sorted(v)))
