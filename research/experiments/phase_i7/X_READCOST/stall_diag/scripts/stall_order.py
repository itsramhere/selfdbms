"""In the frozen paced campaign: first trial, gap to the previous trial, and previous arm."""
import json, glob, os, collections, statistics as st
from datetime import datetime
root = r"C:\Users\ramst\Documents\Projects\selfdbms\research\experiments\phase_i7\X_READCOST\paced\trials"
rows = []
for f in glob.glob(os.path.join(root, "*_a1.json")):
    d = json.load(open(f))
    p = d["result"]["paced_load"]
    rows.append(dict(pos=d["runlist_position"], arm=d["arm"], cell=d["cell"], tid=d["trial_id"],
                     start=datetime.fromisoformat(d["timing"]["started_utc"].rstrip("Z")[:26]), end=datetime.fromisoformat(d["timing"]["finished_utc"].rstrip("Z")[:26]),
                     wmax=max(p["write_service_nanos"]["max"], p["write_service_late_nanos"]["max"]) / 1e6))
rows.sort(key=lambda r: r["pos"])
print("first five trials:", [(r["pos"], r["arm"], round(r["wmax"], 1)) for r in rows[:5]])
gaps = [(rows[i]["start"] - rows[i - 1]["end"]).total_seconds() for i in range(1, len(rows))]
print("gap to previous trial, s: median", round(st.median(gaps), 2), "max", round(max(gaps), 2), "count > 5 s", sum(g > 5 for g in gaps))
print("smallest write maxima:", [(r["tid"], round(r["wmax"], 1)) for r in sorted(rows, key=lambda r: r["wmax"])[:5]])
by = collections.defaultdict(list)
for i in range(1, len(rows)):
    by[(rows[i - 1]["arm"], rows[i]["arm"])].append(rows[i]["wmax"])
print("median write max by (previous arm, this arm):")
for k in sorted(by):
    print(" ", k, len(by[k]), round(st.median(by[k]), 1))
prev = collections.defaultdict(list)
for i in range(1, len(rows)):
    prev[rows[i - 1]["arm"]].append(rows[i]["wmax"])
print("by previous arm:", {k: round(st.median(v), 1) for k, v in prev.items()})
