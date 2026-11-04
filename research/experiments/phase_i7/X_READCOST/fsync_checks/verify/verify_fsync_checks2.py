"""Hold-adjusted Lease/Snapshot read ratio on the frozen settled S3 trials, plus burst counts excluding first fsyncs."""
import glob, json, math, statistics as st
from scipy import stats
X = "C:/Users/ramst/Documents/Projects/selfdbms/research/experiments/phase_i7/X_READCOST"
def free(res):
    d = res["stall_diagnostics"]; h = d["phase_histograms"]; n = d["commit_count"]
    return 1 - sum(h[k]["stats"]["mean"] * n for k in ("validate", "wal_write", "wal_sync", "apply")) / d["window_ns"]
def load(r, a): return json.load(open(f"{X}/settled/trials/S3_d0000_f80_closed_t{r:02d}_{a}_a1.json"))["result"]
raw, adj, fx = [], [], []
for r in range(1, 11):
    a, b = load(r, "AMIN"), load(r, "B")
    y = math.log(a["throughput"]["read_ops_per_sec"] / b["throughput"]["read_ops_per_sec"]); x = math.log(free(a) / free(b))
    raw.append(y); fx.append(x); adj.append(y - x)
for name, v in (("raw", raw), ("adjusted", adj)):
    m, s = st.mean(v), st.stdev(v); h = stats.t.ppf(.95, 9) * s / math.sqrt(10)
    print(f"settled S3 AMIN/B {name}: gm {math.exp(m):.3f} sd {s:.3f} CI {math.exp(m-h):.3f}..{math.exp(m+h):.3f}")
print("pearson free ratio vs read ratio", round(stats.pearsonr(fx, raw)[0], 3))
print("bare, runs with a >=25 ms fsync other than the first fsync:")
bare = [json.load(open(p)) for p in glob.glob(f"{X}/fsync_checks/bare/*.json")]
for rate in (72, 155, 183, 700):
    rs = [b for b in bare if b["rate"] == rate]
    print(" ", rate, sum(any(m >= 25 and t > 0 for t, m in b["slow"]) for b in rs), "of", len(rs), "; first fsync >=25:", sum(b["first_fsync_ms"] >= 25 for b in rs), "; first-fsync median", round(st.median(b["first_fsync_ms"] for b in rs), 1), "range", round(min(b["first_fsync_ms"] for b in rs), 1), round(max(b["first_fsync_ms"] for b in rs), 1))
pl = json.load(open(f"{X}/fsync_checks/plan.json"))
print("first bare position", min(e["order_position"] for e in pl["runs"] if e["part"]=="bare"), "last bare", max(e["order_position"] for e in pl["runs"] if e["part"]=="bare"))
print("700 positions", [e["order_position"] for e in pl["runs"] if e.get("rate")==700])
print("start", json.loads(open(f"{X}/fsync_checks/progress.jsonl").readline())["start_iso"], "end", json.loads(open(f"{X}/fsync_checks/progress.jsonl").readlines()[-1])["end_iso"])
