"""Recompute the fsync check figures from raw files, and test a hold-time account of reads."""
import glob, json, math, os, statistics as st
from scipy import stats

X = "C:/Users/ramst/Documents/Projects/selfdbms/research/experiments/phase_i7/X_READCOST"
F = f"{X}/fsync_checks"

print("Bare, recomputed from run files")
bare = [json.load(open(p)) for p in sorted(glob.glob(f"{F}/bare/*.json"))]
for rate in (72, 155, 183, 700):
    rs = [b for b in bare if b["rate"] == rate]
    print(f"  {rate}: runs {len(rs)} any>=25 {sum(max([m for _, m in b['slow']], default=0) >= 25 for b in rs)} any>=45 {sum(b['fsync_max_ms'] >= 45 for b in rs)} ge25 count {sum(b['count_ge_25'] for b in rs)} ac {set(b['ac_line'] for b in rs)}")
print("  Runs with any fsync >= 25 ms, in run order")
for b in sorted(bare, key=lambda b: b["order_position"]):
    big = [(t, m) for t, m in b["slow"] if m >= 25]
    if big:
        print(f"    pos {b['order_position']:2d} rate {b['rate']:3d} first {b['first_fsync_ms']:.1f}: " + ", ".join(f"{m:.1f}@{t:.2f}" for t, m in big[:8]) + (f" (+{len(big) - 8})" if len(big) > 8 else ""))
pos = [b["order_position"] for b in bare]
any25 = [b["fsync_max_ms"] >= 25 for b in bare]
print("  any>=25 by quarter of the run order:", [sum(a for p, a in zip(pos, any25) if lo < p <= lo + 10) for lo in (0, 10, 20, 30)])


def holds(res):
    d = res["stall_diagnostics"]
    h = d["phase_histograms"]
    n = d["commit_count"]
    # Exclusive hold: validate through apply, in seconds, from the phase means.
    parts = {k: h[k]["stats"]["mean"] * n / 1e9 for k in ("validate", "wal_write", "wal_sync", "apply")}
    big = sum((c["WalSyncDoneNs"] - c["WalWriteDoneNs"]) / 1e9 for c in d["commits"] if c["WalSyncDoneNs"] - c["WalWriteDoneNs"] >= 25e6)
    return sum(parts.values()), parts["wal_sync"], big, d["window_ns"] / 1e9


on, off = {}, {}
for p in glob.glob(f"{F}/engine/F_*.json"):
    if p.endswith(".meta.json"):
        continue
    d = json.load(open(p))
    _, cond, arm, t = os.path.basename(p)[:-5].split("_")
    (on if cond == "on" else off)[arm, t] = d
print("\nRead capacity with fsync off, reads/s")
Roff = {}
for arm in ("AMIN", "B"):
    v = [off[arm, f"t{r:02d}"]["throughput"]["read_ops_per_sec"] for r in range(1, 11)]
    Roff[arm] = st.median(v)
    h = [holds(off[arm, f"t{r:02d}"])[0] / 10 * 100 for r in range(1, 11)]
    print(f"  {arm}: median {Roff[arm]:.0f}, hold share median {st.median(h):.2f}%")


def test(label, recs):
    xs, ys, pred, burst, share = [], [], [], [], []
    for arm, res in recs:
        hold, sync, big, win = holds(res)
        free = 1 - hold / win
        R = res["throughput"]["read_ops_per_sec"]
        # Scale off-capacity to the free part of the window, removing the off-trial's own small hold.
        p = Roff[arm] * free
        xs.append(math.log(free)); ys.append(math.log(R)); pred.append(p); burst.append(big / win * 100); share.append(hold / win * 100)
    err = [math.log(R) - math.log(p) for R, p in zip([math.exp(y) for y in ys], pred)]
    print(f"\n{label}: n {len(xs)}")
    print(f"  hold share %: median {st.median(share):.1f} range {min(share):.1f}..{max(share):.1f}")
    print(f"  fsync>=25 share %: median {st.median(burst):.1f} range {min(burst):.1f}..{max(burst):.1f}")
    print(f"  log R vs log free: pearson {stats.pearsonr(xs, ys)[0]:.3f} spearman {stats.spearmanr(xs, ys)[0]:.3f}; slope {stats.linregress(xs, ys).slope:.3f}")
    print(f"  log R vs burst share: pearson {stats.pearsonr(burst, ys)[0]:.3f}")
    print(f"  hold share vs burst share: pearson {stats.pearsonr(share, burst)[0]:.3f}")
    print(f"  observed R {min(math.exp(y) for y in ys):.0f}..{max(math.exp(y) for y in ys):.0f}; predicted R_off x free {min(pred):.0f}..{max(pred):.0f}")
    print(f"  log(obs/pred): mean {st.mean(err):.3f} sd {st.stdev(err):.3f} (sd log R {st.stdev(ys):.3f})")
    print(f"  residual sd after removing the fit: {st.stdev([y - (stats.linregress(xs, ys).intercept + stats.linregress(xs, ys).slope * x) for x, y in zip(xs, ys)]):.3f}")


test("Fsync on, this check", [(a, on[a, f"t{r:02d}"]) for a in ("AMIN", "B") for r in range(1, 11)])
test("Settled S3, frozen campaign", [(a, json.load(open(f"{X}/settled/trials/S3_d0000_f80_closed_t{r:02d}_{a}_a1.json"))["result"]) for a in ("AMIN", "B") for r in range(1, 11)])

print("\nPer-trial on AMIN and B, ratio of hold-free fractions vs ratio of reads, by round")
xs, ys = [], []
for r in range(1, 11):
    a, b = on["AMIN", f"t{r:02d}"], on["B", f"t{r:02d}"]
    fa, fb = 1 - holds(a)[0] / 10, 1 - holds(b)[0] / 10
    xs.append(math.log(fa / fb)); ys.append(math.log(a["throughput"]["read_ops_per_sec"] / b["throughput"]["read_ops_per_sec"]))
print(f"  pearson {stats.pearsonr(xs, ys)[0]:.3f}, sd of log ratio {st.stdev(ys):.3f}, sd after subtracting free ratio {st.stdev([y - x for x, y in zip(xs, ys)]):.3f}")
adj = [y - x for x, y in zip(xs, ys)]
m, s = st.mean(adj), st.stdev(adj)
h = stats.t.ppf(.95, 9) * s / math.sqrt(10)
print(f"  free-adjusted AMIN/B read ratio gm {math.exp(m):.3f} CI {math.exp(m - h):.3f}..{math.exp(m + h):.3f}")
print("  Note: post hoc; hold time is an outcome measured in the same trial.")
print(f"\nWrite time check: hold means use stats['mean']? sample stats keys: {list(on['AMIN', 't01']['stall_diagnostics']['phase_histograms']['wal_sync']['stats'].keys())}")
