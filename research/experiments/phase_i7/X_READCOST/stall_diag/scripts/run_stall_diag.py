"""Non-campaign stall diagnostics. Exploratory; nothing here is pre-registered.

36 runs in a seeded shuffled order on the campaign's volume:
fsync on: (d=0,f=.2), (d=2000,f=.2), (d=0,f=.8) x 3 arms x 3 specs = 27
fsync off: (d=0,f=.2) x 3 arms x 3 specs = 9
"""
import json, os, random, shutil, subprocess, sys, time

ROOT = r"C:\Users\ramst\Documents\Projects\selfdbms\research\experiments\phase_i7\X_READCOST"
TRIALS = os.path.join(ROOT, "paced", "trials")
OUT = os.path.join(ROOT, "stall_diag")
RUNS = os.path.join(OUT, sys.argv[2] if len(sys.argv) > 2 else "runs")
DATA = os.path.join(OUT, "data")
EXE = sys.argv[1]
os.makedirs(RUNS, exist_ok=True)

plan = []
for sync, cells in (("true", ["d0000_f20", "d2000_f20", "d0000_f80"]), ("false", ["d0000_f20"])):
    for cell in cells:
        for arm in ("A", "AMIN", "B"):
            for t in (1, 2, 3):
                plan.append((cell, arm, t, sync))
random.Random(20261103).shuffle(plan)

t0 = time.time()
log = open(os.path.join(RUNS, "order.txt"), "w")
for n, (cell, arm, t, sync) in enumerate(plan):
    tid = f"D_{cell}_closed_t{t:02d}_{arm}_sync{sync}"
    spec = os.path.join(TRIALS, f"P1_{cell}_closed_t{t:02d}_{arm}_a1.json")
    out = os.path.join(RUNS, tid + ".json")
    data = os.path.join(DATA, tid)
    start = time.time()
    r = subprocess.run([EXE, "-spec", spec, "-out", out, "-data-dir", data, "-wal-sync", sync, "-trial-id", tid], capture_output=True, text=True)
    line = f"{n + 1}/{len(plan)} {tid} exit={r.returncode} wall={time.time() - start:.1f}s start_since={start - t0:.1f}s {r.stderr.strip()[:200]}"
    print(line, flush=True)
    log.write(line + "\n")
    log.flush()
    shutil.rmtree(data, ignore_errors=True)
print("done", round(time.time() - t0), "s")
