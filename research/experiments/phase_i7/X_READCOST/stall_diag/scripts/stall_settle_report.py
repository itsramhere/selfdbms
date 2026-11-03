"""Settled vs back-to-back, and delete vs no delete in the bare probe."""
import json, glob, os, statistics as st
D = r"C:\Users\ramst\Documents\Projects\selfdbms\research\experiments\phase_i7\X_READCOST\stall_diag\settle"
print("Part A: engine, sync durations (ms) from the commit trace")
print(f"{'arm':<6}{'t':>3} | {'settled max':>12}{'at s':>7}{'>=10':>6}{'>=25':>6} | {'b2b max':>10}{'at s':>7}{'>=10':>6}{'>=25':>6} | {'settled wmax':>13}{'b2b wmax':>10}")
sm, bm = [], []
for f in sorted(glob.glob(os.path.join(D, "S_settled_*.json"))):
    name = os.path.basename(f)
    g = os.path.join(D, name.replace("S_settled_", "S_back_to_back_"))
    arm, t = name[len("S_settled_"):-5].rsplit("_t", 1)
    out = []
    for p in (f, g):
        d = json.load(open(p))
        cs = d["stall_diagnostics"]["commits"]
        sy = [((c["WalSyncDoneNs"] - c["WalWriteDoneNs"]) / 1e6, c["ArrivalNs"] / 1e9) for c in cs]
        m = max(sy)
        out.append((m[0], m[1], sum(x >= 10 for x, _ in sy), sum(x >= 25 for x, _ in sy), d["paced_load"]["write_service_nanos"]["max"] / 1e6))
    print(f"{arm:<6}{t:>3} | {out[0][0]:>12.1f}{out[0][1]:>7.2f}{out[0][2]:>6}{out[0][3]:>6} | {out[1][0]:>10.1f}{out[1][1]:>7.2f}{out[1][2]:>6}{out[1][3]:>6} | {out[0][4]:>13.1f}{out[1][4]:>10.1f}")
    sm.append(out[0][0])
    bm.append(out[1][0])
print("median max sync: settled", round(st.median(sm), 1), " back-to-back", round(st.median(bm), 1))
print("pairs where back-to-back max is larger:", sum(b > s for s, b in zip(sm, bm)), "of", len(sm))
print("sorted settled:", sorted(round(x, 1) for x in sm))
print("sorted b2b:    ", sorted(round(x, 1) for x in bm))
print("\nPart B: bare probe, delete at t=15 s")
print(f"{'tag':<8}{'rep':>4} | {'max 0-15':>9}{'max 15-30':>10}{'max 30-35':>10} | stalls >=20 ms (s, ms)")
for f in sorted(glob.glob(os.path.join(D, "B_*.json"))):
    d = json.load(open(f))
    w = lambda a, b: max([ms for s, ms in d["slow"] if a <= s < b] or [0])
    big = [(round(s, 1), round(ms)) for s, ms in d["slow"] if ms >= 20]
    print(f"{d['tag']:<8}{d['rep']:>4} | {w(0,15):>9.1f}{w(15,30):>10.1f}{w(30,35):>10.1f} | {big[:12]}{' ...' if len(big) > 12 else ''}")
