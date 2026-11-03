"""Non-campaign: do the large fsync stalls follow the previous trial's cleanup?

Part A, engine: nine pairs. Each pair waits 30 s, runs a "settled" trial, then runs
the same spec straight after its cleanup ("back_to_back"), as the campaigns did.
Part B, bare: fsync at the paced rate for 35 s after a 25 s wait, no engine. At
t=15 s either nothing happens or a directory shaped like a trial's data is deleted.
The directory is created and synced before the wait, so only the delete falls
inside the run. The two conditions alternate.
"""
import json, os, shutil, subprocess, sys, threading, time

ROOT = r"C:\Users\ramst\Documents\Projects\selfdbms\research\experiments\phase_i7\X_READCOST"
TRIALS = os.path.join(ROOT, "paced", "trials")
OUT = os.path.join(ROOT, "stall_diag", "settle")
DATA = os.path.join(ROOT, "stall_diag", "data")
EXE = sys.argv[1]
os.makedirs(OUT, exist_ok=True)
os.makedirs(DATA, exist_ok=True)
RATE, PAYLOAD = 183, b"x" * 140


def trial(tag, arm, t):
    tid = f"S_{tag}_{arm}_t{t:02d}"
    data = os.path.join(DATA, tid)
    spec = os.path.join(TRIALS, f"P1_d0000_f20_closed_t{t:02d}_{arm}_a1.json")
    r = subprocess.run([EXE, "-spec", spec, "-out", os.path.join(OUT, tid + ".json"), "-data-dir", data, "-trial-id", tid], capture_output=True, text=True)
    print(tid, "exit", r.returncode, r.stderr[:200], flush=True)
    shutil.rmtree(data, ignore_errors=True)


pairs = [("A", 1), ("AMIN", 2), ("B", 3), ("AMIN", 1), ("B", 2), ("A", 3), ("B", 1), ("A", 2), ("AMIN", 3)]
for arm, t in pairs:
    time.sleep(30)
    trial("settled", arm, t)
    trial("back_to_back", arm, t)


def make_dir(path):
    os.makedirs(path)
    for i, size in enumerate((400_000, 400_000, 400_000, 400_000, 300_000, 150_000)):
        fd = os.open(os.path.join(path, f"f{i}.dat"), os.O_CREAT | os.O_WRONLY | os.O_BINARY)
        os.write(fd, b"y" * size)
        os.fsync(fd)
        os.close(fd)


def bare(tag, rep):
    victim = os.path.join(DATA, f"victim_{tag}_{rep}")
    make_dir(victim)
    time.sleep(25)
    path = os.path.join(DATA, f"bare_{tag}_{rep}.wal")
    fd = os.open(path, os.O_CREAT | os.O_WRONLY | os.O_BINARY | os.O_APPEND)
    start = time.perf_counter()
    if tag == "delete":
        threading.Timer(15.0, shutil.rmtree, (victim,)).start()
    samples, i = [], 0
    while i / RATE < 35.0:
        while time.perf_counter() - start < i / RATE:
            time.sleep(0)
        os.write(fd, PAYLOAD)
        a = time.perf_counter()
        os.fsync(fd)
        b = time.perf_counter()
        samples.append((a - start, (b - a) * 1e3))
        i += 1
    os.close(fd)
    os.remove(path)
    shutil.rmtree(victim, ignore_errors=True)
    d = sorted(ms for _, ms in samples)
    slow = [[round(s, 3), round(ms, 2)] for s, ms in samples if ms >= 3]
    json.dump({"tag": tag, "rep": rep, "n": len(d), "p50": d[len(d) // 2], "max": d[-1], "slow": slow}, open(os.path.join(OUT, f"B_{tag}_{rep}.json"), "w"))
    print("bare", tag, rep, "max", round(d[-1], 1), flush=True)


for rep in range(4):
    for tag in (("none", "delete") if rep % 2 == 0 else ("delete", "none")):
        bare(tag, rep)
print("done")
