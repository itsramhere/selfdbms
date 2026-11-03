"""Non-campaign calibration before freezing X-settled-v1: how long a pause removes the carry-over?

One run of trials at d=0, f=0.2, closed. Before each trial, pause for a level taken
from a seeded shuffle of six levels x six repeats. The previous trial is always an
ordinary trial of the same setting. These specs and trials are excluded from analysis.
"""
import os, random, shutil, subprocess, sys, time

ROOT = r"C:\Users\ramst\Documents\Projects\selfdbms\research\experiments\phase_i7\X_READCOST"
TRIALS = os.path.join(ROOT, "paced", "trials")
OUT = os.path.join(ROOT, "stall_diag", "pause")
DATA = os.path.join(ROOT, "stall_diag", "data")
EXE = sys.argv[1]
os.makedirs(OUT, exist_ok=True)
os.makedirs(DATA, exist_ok=True)
LEVELS = [0, 5, 10, 15, 20, 30]
order = LEVELS * 6
random.Random(20261104).shuffle(order)
arms = ["A", "AMIN", "B"]


def trial(tid, arm, t):
    data = os.path.join(DATA, tid)
    spec = os.path.join(TRIALS, f"P1_d0000_f20_closed_t{t:02d}_{arm}_a1.json")
    r = subprocess.run([EXE, "-spec", spec, "-out", os.path.join(OUT, tid + ".json"), "-data-dir", data, "-trial-id", tid], capture_output=True, text=True)
    print(tid, "exit", r.returncode, r.stderr[:200], flush=True)
    shutil.rmtree(data, ignore_errors=True)


trial("C_prime", "A", 10)  # so the first measured trial also has a trial before it
os.remove(os.path.join(OUT, "C_prime.json"))
for n, pause in enumerate(order):
    time.sleep(pause)
    trial(f"C_{n:02d}_p{pause:02d}", arms[n % 3], 4 + n % 6)
print("done")
