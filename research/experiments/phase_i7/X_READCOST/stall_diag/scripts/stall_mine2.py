"""Where does the existing evidence place the ~55 ms write stall?

Per trial: largest write service, largest commit lock wait (first hold only),
largest read latency, largest read critical section. If a stall sits inside the
exclusive hold, a reader blocked on RLock must see a read about that long.
"""
import json, glob, os, re, collections, statistics as st
root = r"C:\Users\ramst\Documents\Projects\selfdbms\research\experiments\phase_i7\X_READCOST\paced\trials"
rows = []
for f in sorted(glob.glob(os.path.join(root, "*_a1.json"))):
    d = json.load(open(f))
    r = d["result"]
    m = re.match(r"P1_d(\d+)_f(\d+)_(closed|open)_t(\d+)_(A|AMIN|B)_a1", os.path.basename(f))
    p = r["paced_load"]
    s = r["sync_instrumentation"]
    wl = s["writer_lock_wait_nanos"]["commit"]
    cs = next(iter(s["read_critical_section_nanos"].values()))
    lr = r["latency_read_nanos"]
    w, wl_late = p["write_service_nanos"], p["write_service_late_nanos"]
    rows.append(dict(
        d=int(m[1]), f=int(m[2]), mode=m[3], t=int(m[4]), arm=m[5],
        wmax=max(w["max"], wl_late["max"]) / 1e6, wp999=(w["p999"] or 0) / 1e6, wp99=(w["p99"] or 0) / 1e6, wp50=(w["p50"] or 0) / 1e6,
        lockmax=wl["max_nanos"] / 1e6, csmax=cs["max_nanos"] / 1e6,
        rmax=lr["max"] / 1e6, rp999=(lr.get("p999") or 0) / 1e6,
        gc=d.get("runtime"), flushes=r["lsm_diagnostics"]["measured_flushes"],
        comp=r["lsm_diagnostics"]["measured_compactions"], n=w["sample_count"],
    ))
print("runtime block example:", json.dumps(rows[0]["gc"])[:600])
med = lambda xs: round(st.median(xs), 2)
print("\ncell: median per-trial maxima in ms")
print(f"{'d':>5} {'f':>3} {'mode':>6} {'arm':>5} {'wmax':>7} {'lockmax':>8} {'rmax':>7} {'csmax':>7} {'wp999':>7} {'flush':>6}")
g = collections.defaultdict(list)
for x in rows:
    g[(x["d"], x["f"], x["mode"], x["arm"])].append(x)
for k in sorted(g):
    v = g[k]
    print(f"{k[0]:>5} {k[1]:>3} {k[2]:>6} {k[3]:>5} {med([x['wmax'] for x in v]):>7} {med([x['lockmax'] for x in v]):>8} {med([x['rmax'] for x in v]):>7} {med([x['csmax'] for x in v]):>7} {med([x['wp999'] for x in v]):>7} {med([x['flushes'] for x in v]):>6}")
print("\nFraction of trials with: lock wait >= half the write max")
for arm in ("A", "AMIN", "B"):
    v = [x for x in rows if x["arm"] == arm]
    print(arm, sum(x["lockmax"] >= 0.5 * x["wmax"] for x in v), "/", len(v))
print("Fraction of trials with: read max >= 80% of write max (reader saw the stall)")
for arm in ("A", "AMIN", "B"):
    v = [x for x in rows if x["arm"] == arm]
    print(arm, sum(x["rmax"] >= 0.8 * x["wmax"] for x in v), "/", len(v))
print("Median ratio rmax/wmax by arm:", {a: med([x["rmax"] / x["wmax"] for x in rows if x["arm"] == a]) for a in ("A", "AMIN", "B")})
print("Median ratio csmax/wmax by arm:", {a: med([x["csmax"] / x["wmax"] for x in rows if x["arm"] == a]) for a in ("A", "AMIN", "B")})
print("wmax vs flushes: trials with zero flushes", sum(x["flushes"] == 0 for x in rows), "median wmax", med([x["wmax"] for x in rows if x["flushes"] == 0]) if any(x["flushes"] == 0 for x in rows) else None)
print("trials with >0 flushes", sum(x["flushes"] > 0 for x in rows), "median wmax", med([x["wmax"] for x in rows if x["flushes"] > 0]) if any(x["flushes"] > 0 for x in rows) else None)
print("compactions total", sum(x["comp"] for x in rows))
