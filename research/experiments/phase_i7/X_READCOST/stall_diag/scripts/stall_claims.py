"""Does the fsync stall touch each tail or throughput claim? Exploratory; no new tests."""
import json, glob, os, re, math, collections, statistics as st
X = r"C:\Users\ramst\Documents\Projects\selfdbms\research\experiments\phase_i7\X_READCOST"
RUNS = os.path.join(X, "stall_diag", "runs")
gm = lambda xs: math.exp(st.mean(math.log(x) for x in xs))

print("=== 1. Phase medians by arm and cell, from whole-window histograms (ms), medians over 3 runs")
g = collections.defaultdict(list)
for f in glob.glob(os.path.join(RUNS, "*.json")):
    d = json.load(open(f))
    m = re.match(r"D_d(\d+)_f(\d+)_closed_t(\d+)_(A|AMIN|B)_sync(\w+)", d["trial_id"])
    g[(int(m[1]), int(m[2]), m[5], m[4])].append(d)
names = ["lock_wait", "validate", "wal_write", "wal_sync", "apply", "flush_check", "total"]
print(f"{'d':>5}{'f':>4}{'sync':>6}{'arm':>6} " + " ".join(f"{n[:9]:>9}" for n in names) + f"{'resp_p50':>9}{'svc_mean':>9}{'stall%':>8}")
for k in sorted(g):
    v = g[k]
    row = [st.median(x["stall_diagnostics"]["phase_histograms"][n]["stats"]["p50"] / 1e6 for x in v) for n in names]
    resp = st.median(x["paced_load"]["write_response_nanos"]["p50"] / 1e6 for x in v)
    svc = st.median(x["paced_load"]["write_service_nanos"]["mean"] / 1e6 for x in v)
    stall = []
    for x in v:
        tot = sum(c["WalSyncDoneNs"] - c["WalWriteDoneNs"] for c in x["stall_diagnostics"]["commits"] if c["WalSyncDoneNs"] - c["WalWriteDoneNs"] >= 10e6)
        stall.append(100 * tot / x["stall_diagnostics"]["window_ns"])
    print(f"{k[0]:>5}{k[1]:>4}{k[2]:>6}{k[3]:>6} " + " ".join(f"{a:>9.3f}" for a in row) + f"{resp:>9.3f}{svc:>9.3f}{st.median(stall):>8.2f}")

print("\n=== 2. Replay at f=0.2: does removing stalls change write response p99? (one queue per trial)")
def replay(d, cap_ms=None):
    s = d["stall_diagnostics"]
    rate = d["config"]["paced_write_rate_per_sec"]
    period = 1e9 / rate
    base = s["phase_histograms"]["total"]["stats"]["p50"]
    slow = {c["index"]: c["FlushCheckDoneNs"] - c["ArrivalNs"] for c in s["commits"]}
    n = s["commit_count"]
    finish, out = 0.0, []
    for i in range(n):
        sv = slow.get(i + 1, base)
        if cap_ms is not None and sv >= cap_ms * 1e6:
            sv = base
        t = i * period
        finish = max(finish, t) + sv
        out.append(finish - t)
    out.sort()
    return out[int(0.99 * len(out))] / 1e6, out[int(0.95 * len(out))] / 1e6, out[len(out) // 2] / 1e6
for key in sorted(k for k in g if k[1] == 20 and k[2] == "true"):
    for d in g[key]:
        p = d["paced_load"]["write_response_nanos"]
        a, a95, a50 = replay(d)
        b, b95, b50 = replay(d, 25)
        print(f"{key[0]:>5} {key[3]:>5} {d['trial_id'][-20:]:>22} observed p99 {p['p99']/1e6:>7.2f}  replay p99 {a:>7.2f}  stalls removed p99 {b:>7.2f}  p95 {a95:>6.2f}->{b95:>6.2f}  p50 {a50:>6.2f}->{b50:>6.2f}")

print("\n=== 3. Paced campaign: stability of write-response percentiles, CoarseLock/Lease, f=0.2")
rows = {}
for f in glob.glob(os.path.join(X, "paced", "trials", "*_a1.json")):
    d = json.load(open(f))
    m = re.match(r"P1_d(\d+)_f(\d+)_(closed|open)_t(\d+)_(A|AMIN|B)_a1", os.path.basename(f))
    rows[(int(m[1]), int(m[2]), m[3], int(m[4]), m[5])] = d["result"]
print(f"{'d':>5}{'mode':>7} | " + " ".join(f"{'gm ' + q:>8}{'sdlog':>6}" for q in ("p50", "p95", "p99", "p999")))
for d_ in (0, 200, 2000):
    for mode in ("closed", "open"):
        cells = []
        for q in ("p50", "p95", "p99", "p999"):
            pairs = [(rows[(d_, 20, mode, t, "A")]["paced_load"]["write_response_nanos"][q], rows[(d_, 20, mode, t, "AMIN")]["paced_load"]["write_response_nanos"][q]) for t in range(1, 11)]
            logs = [math.log(a / b) for a, b in pairs if a and b]
            cells.append(f"{math.exp(st.mean(logs)):>8.3f}{st.stdev(logs):>6.2f}" if len(logs) > 1 else f"{'n/a':>8}{'':>6}")
        print(f"{d_:>5}{mode:>7} | " + " ".join(cells))

print("\n=== 4. The one inconclusive read comparison, d=0 f=0.8 closed: does read loss follow write excess?")
print("  round  arm reads/s  excess_write_s  write_p999_ms")
xs, ys = [], []
for t in range(1, 11):
    for arm in ("AMIN", "B"):
        r = rows[(0, 80, "closed", t, arm)]
        w = r["paced_load"]["write_service_nanos"]
        excess = (w["mean"] - w["p50"]) * w["sample_count"] / 1e9
        reads = r["operations"]["read_ops_completed"] / 10
        print(f"  {t:>5} {arm:>4} {reads:>9.0f} {excess:>8.2f} {w['p999']/1e6:>10.1f}")
        xs.append(excess)
        ys.append(math.log(reads))
mx, my = st.mean(xs), st.mean(ys)
print("  corr(excess, log reads) =", round(sum((a - mx) * (b - my) for a, b in zip(xs, ys)) / math.sqrt(sum((a - mx) ** 2 for a in xs) * sum((b - my) ** 2 for b in ys)), 3))
rat = []
for t in range(1, 11):
    a, b = rows[(0, 80, "closed", t, "AMIN")], rows[(0, 80, "closed", t, "B")]
    ex = lambda r: (r["paced_load"]["write_service_nanos"]["mean"] - r["paced_load"]["write_service_nanos"]["p50"]) * r["paced_load"]["write_service_nanos"]["sample_count"] / 1e9
    rat.append((round(a["operations"]["read_ops_completed"] / b["operations"]["read_ops_completed"], 3), round(ex(a) - ex(b), 2)))
print("  per round (L/S read ratio, L minus S excess seconds):", rat)

print("\n=== 5. Absolute size of other tail claims vs the stall")
for name, pat, keys in (("intervention", "intervention", None), ("compaction", "compaction", None)):
    fs = glob.glob(os.path.join(X, pat, "trials", "*_a1.json"))
    print(name, "trials", len(fs))
    p99s, cw, reads, wr = [], [], [], []
    for f in fs:
        r = json.load(open(f))["result"]
        p99s.append(r["latency_read_nanos"]["p99"] / 1e6)
        reads.append(r["operations"]["read_ops_completed"])
        wr.append(r["operations"]["write_ops_completed"])
        c = r["sync_instrumentation"]["writer_lock_wait_nanos"].get("commit")
        if c:
            cw.append(c["p99_nanos"] / 1e6)
    q = lambda v, p: sorted(v)[int(p * (len(v) - 1))]
    print(f"  read p99 ms: median {st.median(p99s):.2f}, 90th {q(p99s,.9):.2f}, max {max(p99s):.2f}; min reads in a trial {min(reads)}, 1% of that {min(reads)//100}")
    if cw:
        print(f"  commit-wait p99 ms: median {st.median(cw):.3f}, 90th {q(cw,.9):.3f}, max {max(cw):.2f}")
