"""Stall times in 60 s engine and bare runs."""
import json, glob, os
D = r"C:\Users\ramst\Documents\Projects\selfdbms\research\experiments\phase_i7\X_READCOST\stall_diag\long"
for f in sorted(glob.glob(os.path.join(D, "L_*.json"))):
    if f.endswith(".spec.json"):
        continue
    d = json.load(open(f))
    if "stall_diagnostics" in d:
        s = d["stall_diagnostics"]
        sl = sorted((c["ArrivalNs"] / 1e9, (c["WalSyncDoneNs"] - c["WalWriteDoneNs"]) / 1e6) for c in s["commits"])
    else:
        sl = [tuple(x) for x in d["slow"]]
    big = [(round(a, 1), round(b, 1)) for a, b in sl if b >= 20]
    gaps = [round(big[i + 1][0] - big[i][0], 1) for i in range(len(big) - 1)]
    print(os.path.basename(f))
    print("  sync >=20 ms:", len(big), big)
    print("  gaps s:", gaps)
    print("  count >=3 ms:", len(sl), " >=10 ms:", sum(b >= 10 for _, b in sl), " >=40 ms:", sum(b >= 40 for _, b in sl), " max:", round(max(b for _, b in sl), 1))
