"""Recompute X-settled-v1 key numbers from raw trial JSON, without the campaign analysis module."""
import glob, json, math, os, statistics as st, sys
from scipy import stats

X = "C:/Users/ramst/Documents/Projects/selfdbms/research/experiments/phase_i7/X_READCOST"
T = {}
for p in glob.glob(f"{X}/settled/trials/*_a1.json"):
    d = json.load(open(p))
    T[d["trial_id"]] = d
print("trials", len(T), "status", {d["status"] for d in T.values()}, "revs", {d["result"]["git_revision"][:7] for d in T.values()})

MARGIN = math.log(1 / 0.9)


def get(cell, r, arm):
    return T[f"{cell}_t{r:02d}_{arm}"]["result"]


def wr(res, q):
    return res["paced_load"]["write_response_nanos"][q]


def phase(res, name, q="p50"):
    return res["stall_diagnostics"]["phase_histograms"][name]["stats"][q]


def ci(xs, level=0.90):
    m, s, n = st.mean(xs), st.stdev(xs), len(xs)
    h = stats.t.ppf(0.5 + level / 2, n - 1) * s / math.sqrt(n)
    return m, s, m - h, m + h


def ratio_line(label, xs):
    m, s, lo, hi = ci(xs)
    print(f"  {label}: gm {math.exp(m):.3f} sd_log {s:.3f} 90%CI {math.exp(lo):.3f}..{math.exp(hi):.3f}")


print("\nH-M1/M2: A/AMIN write response p50")
for mode in ("closed", "open"):
    rows = []
    for dose in ("0000", "0200", "2000"):
        c = f"S1_d{dose}_f20_{mode}"
        logs = [math.log(wr(get(c, r, "A"), "p50") / wr(get(c, r, "AMIN"), "p50")) for r in range(1, 11)]
        rows.append(logs)
        ratio_line(c, logs)
    ranks = [stats.rankdata(col) for col in zip(*rows)]
    L = sum(rk[j] * (j + 1) for rk in ranks for j in range(3))
    pt = stats.page_trend_test([list(col) for col in zip(*rows)], ranked=False, predicted_ranks=[1, 2, 3], method="auto")
    print(f"  Page {mode}: L by hand {L}, scipy L {pt.statistic} p {pt.pvalue:.3g} {pt.method}")

print("\nH-M3 at S1_d2000_f20_closed (ms, mean of p50, 95% t CI)")
for arm in ("A", "AMIN"):
    for name in ("lock_wait", "flush_check"):
        v = [phase(get("S1_d2000_f20_closed", r, arm), name) / 1e6 for r in range(1, 11)]
        m, s, lo, hi = ci(v, 0.95)
        print(f"  {arm} {name}: {m:.4f} ({lo:.4f}, {hi:.4f}) range {min(v):.4f}..{max(v):.4f}")

print("\nCommit phases, mean of per-trial p50 (ms)")
for c in [f"S1_d{d}_f20_{m}" for d in ("0000", "0200", "2000") for m in ("closed", "open")] + ["S2_d0200_w700_closed", "S2_d0200_w700_open", "S3_d0000_f80_closed"]:
    for arm in ("A", "AMIN", "B"):
        rs = [get(c, r, arm) for r in range(1, 11)]
        vals = {n: st.mean(phase(x, n) for x in rs) / 1e6 for n in ("lock_wait", "validate", "wal_sync", "apply", "flush_check", "total")}
        print(f"  {c} {arm}: " + " ".join(f"{k} {v:.3f}" for k, v in vals.items()))

print("\nH-W, H-R")
for c in ("S2_d0200_w700_closed", "S2_d0200_w700_open"):
    ratio_line(f"{c} W A/AMIN", [math.log(get(c, r, "A")["throughput"]["write_ops_per_sec"] / get(c, r, "AMIN")["throughput"]["write_ops_per_sec"]) for r in range(1, 11)])
    for arm in ("A", "AMIN", "B"):
        print(f"    {arm} W mean {st.mean(get(c, r, arm)['throughput']['write_ops_per_sec'] for r in range(1, 11)):.1f}, R mean {st.mean(get(c, r, arm)['throughput']['read_ops_per_sec'] for r in range(1, 11)):.0f}, unissued ticks mean {st.mean(get(c, r, arm)['paced_load']['write_ticks_unissued'] for r in range(1, 11)):.1f}, write resp p50 ms {st.mean(wr(get(c, r, arm), 'p50') for r in range(1, 11)) / 1e6:.2f}")
c = "S3_d0000_f80_closed"
for label, key in (("R", "read_ops_per_sec"), ("W", "write_ops_per_sec")):
    logs = [math.log(get(c, r, "AMIN")["throughput"][key] / get(c, r, "B")["throughput"][key]) for r in range(1, 11)]
    m, s, lo, hi = ci(logs)
    tost = max(1 - stats.t.cdf((m + MARGIN) / (s / math.sqrt(10)), 9), stats.t.cdf((m - MARGIN) / (s / math.sqrt(10)), 9))
    print(f"  S3 {label} AMIN/B gm {math.exp(m):.3f} sd {s:.3f} CI {math.exp(lo):.3f}..{math.exp(hi):.3f} tost p {tost:.3g} holm(x2 at most) {min(1, tost * 2):.3g}")

xs, ys = [], []
for r in range(1, 11):
    for arm in ("A", "AMIN", "B"):
        x = get(c, r, arm)
        n = x["paced_load"]["write_response_nanos"]
        xs.append((n["mean"] - n["p50"]) * n["sample_count"])
        ys.append(math.log(x["throughput"]["read_ops_per_sec"]))
print(f"  S3 log reads vs write excess: r {stats.pearsonr(xs, ys)[0]:.3f}, spearman {stats.spearmanr(xs, ys)[0]:.3f}, R range {math.exp(min(ys)):.0f}..{math.exp(max(ys)):.0f}")

print("\nH-S and fsync")
s1 = [f"S1_d{d}_f20_{m}" for d in ("0000", "0200", "2000") for m in ("closed", "open")]
allmax = []
for cell in s1:
    for arm in ("A", "AMIN", "B"):
        v = [phase(get(cell, r, arm), "wal_sync", "max") / 1e6 for r in range(1, 11)]
        allmax += v
        print(f"  {cell} {arm}: median {st.median(v):.1f} range {min(v):.1f}..{max(v):.1f} n>=25 {sum(x >= 25 for x in v)} n>=45 {sum(x >= 45 for x in v)}")
print(f"  all S1 trials n={len(allmax)}: median {st.median(allmax):.1f} max {max(allmax):.1f} >=25ms {sum(x >= 25 for x in allmax)} >=45ms {sum(x >= 45 for x in allmax)}")
allx = []
for tid, d in T.items():
    if d["cell"].startswith(("S2", "S3")):
        allx.append(phase(d["result"], "wal_sync", "max") / 1e6)
print(f"  S2+S3 trials n={len(allx)}: median {st.median(allx):.1f} max {max(allx):.1f} >=25ms {sum(x >= 25 for x in allx)} >=45ms {sum(x >= 45 for x in allx)}")
bands = {"<25": 0, "25-40": 0, "40-45": 0, ">=45": 0}
for x in allmax + allx:
    bands["<25" if x < 25 else "25-40" if x < 40 else "40-45" if x < 45 else ">=45"] += 1
print("  bands all paced", bands)

print("\nTails: A/AMIN write response, absolute ms (mean of per-trial)")
for cell in s1:
    for q in ("p50", "p95", "p99"):
        logs = [math.log(wr(get(cell, r, "A"), q) / wr(get(cell, r, "AMIN"), q)) for r in range(1, 11)]
        a = st.mean(wr(get(cell, r, "A"), q) for r in range(1, 11)) / 1e6
        b = st.mean(wr(get(cell, r, "AMIN"), q) for r in range(1, 11)) / 1e6
        s_ = st.mean(wr(get(cell, r, "B"), q) for r in range(1, 11)) / 1e6
        m, s, lo, hi = ci(logs)
        print(f"  {cell} {q}: A {a:.2f} AMIN {b:.2f} B {s_:.2f} ratio {math.exp(m):.3f} sd_log {s:.3f} CI {math.exp(lo):.3f}..{math.exp(hi):.3f}")

print("\nAMIN/B write response p50 (Lease vs Snapshot)")
for cell in s1:
    ratio_line(cell, [math.log(wr(get(cell, r, "AMIN"), "p50") / wr(get(cell, r, "B"), "p50")) for r in range(1, 11)])

print("\nAcross campaign, write response A/AMIN, new vs paced (from raw paced trials)")
P = {}
for p in glob.glob(f"{X}/paced/trials/*_a1.json"):
    d = json.load(open(p))
    if d["status"] == "SUCCESS":
        P[d["trial_id"]] = d
print("  paced trials", len(P))
for cell in s1 + ["S3_d0000_f80_closed"]:
    pc = cell.replace("S1_", "P1_").replace("S3_", "P1_")
    for q in ("p50", "p95", "p99"):
        old = [math.log(wr(P[f"{pc}_t{r:02d}_A"]["result"], q) / wr(P[f"{pc}_t{r:02d}_AMIN"]["result"], q)) for r in range(1, 11)]
        new = [math.log(wr(get(cell, r, "A"), q) / wr(get(cell, r, "AMIN"), q)) for r in range(1, 11)]
        print(f"  {cell} {q}: paced gm {math.exp(st.mean(old)):.3f} sd {st.stdev(old):.3f} | settled gm {math.exp(st.mean(new)):.3f} sd {st.stdev(new):.3f}")
    oldmax = [P[f"{pc}_t{r:02d}_{a}"]["result"]["paced_load"]["write_service_nanos"]["max"] / 1e6 for r in range(1, 11) for a in ("A", "AMIN", "B")]
    newmax = [get(cell, r, a)["paced_load"]["write_service_nanos"]["max"] / 1e6 for r in range(1, 11) for a in ("A", "AMIN", "B")]
    print(f"    largest write service: paced median {st.median(oldmax):.1f} range {min(oldmax):.1f}..{max(oldmax):.1f} | settled median {st.median(newmax):.1f} range {min(newmax):.1f}..{max(newmax):.1f}")

print("\nSentinels from raw")
ORIG = {"R_X1_r16_d0020": ("campaign", "X1_r16_d0020"), "R_X1_r16_d2000": ("campaign", "X1_r16_d2000"), "R_X3_r16": ("campaign", "X3_r16"), "R_C1_r16_d0200_on": ("compaction", "C1_r16_d0200_on"), "R_T1_n256_r16_w0": ("tablecount", "T1_n256_r16_w0")}
for cell, (src, oc) in ORIG.items():
    logs = [math.log(get(cell, r, "AMIN")["throughput"]["read_ops_per_sec"] / get(cell, r, "B")["throughput"]["read_ops_per_sec"]) for r in range(1, 11)]
    ratio_line(f"{cell} R AMIN/B", logs)
    for arm in ("A", "AMIN", "B"):
        rs = [get(cell, r, arm) for r in range(1, 11)]
        print(f"    {arm} W {st.mean(x['throughput']['write_ops_per_sec'] for x in rs):.1f} R {st.mean(x['throughput']['read_ops_per_sec'] for x in rs):.0f}")

print("\nC1 commit wait p99 AMIN and B (ms)")
for root, cell in ((f"{X}/settled", "R_C1_r16_d0200_on"), (f"{X}/compaction", "C1_r16_d0200_on")):
    for arm in ("A", "AMIN", "B"):
        v = []
        for r in range(1, 11):
            d = json.load(open(f"{root}/trials/{cell}_t{r:02d}_{arm}_a1.json"))
            v.append(d["result"]["sync_instrumentation"]["writer_lock_wait_nanos"]["commit"]["p99"] / 1e6)
        print(f"  {cell} {arm}: median {st.median(v):.3f} max {max(v):.3f} min {min(v):.3f}")
