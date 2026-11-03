"""Non-campaign: is the stall a one-off after setup, or does it recur? Alternating 60 s runs.

engine: paced d=0 f=0.2 closed, fsync on, traced. bare: append+fsync at the same
rate with no engine, no readers, on the same directory tree. Stall times are
compared in the two; the bare run has no fixture, so a stall there can't be a
fixture after-effect.
"""
import json, os, shutil, subprocess, sys, time

ROOT = r"C:\Users\ramst\Documents\Projects\selfdbms\research\experiments\phase_i7\X_READCOST"
OUT = os.path.join(ROOT, "stall_diag", "long")
DATA = os.path.join(ROOT, "stall_diag", "data")
EXE = sys.argv[1]
os.makedirs(OUT, exist_ok=True)
os.makedirs(DATA, exist_ok=True)
RATE, SECONDS, PAYLOAD = 183, 60.0, b"x" * 140


def engine(arm, t):
    rec = json.load(open(os.path.join(ROOT, "paced", "trials", f"P1_d0000_f20_closed_t{t:02d}_{arm}_a1.json")))
    spec = rec["spec"]
    spec["measurement_duration_ms"] = int(SECONDS * 1000)
    spec["trial_id"] = f"L_engine_{arm}_t{t:02d}"
    path = os.path.join(OUT, spec["trial_id"] + ".spec.json")
    json.dump(spec, open(path, "w"))
    data = os.path.join(DATA, spec["trial_id"])
    r = subprocess.run([EXE, "-spec", path, "-out", os.path.join(OUT, spec["trial_id"] + ".json"), "-data-dir", data], capture_output=True, text=True)
    print("engine", arm, t, "exit", r.returncode, r.stderr[:200], flush=True)
    shutil.rmtree(data, ignore_errors=True)


def bare(t):
    path = os.path.join(DATA, f"bare_{t}.wal")
    fd = os.open(path, os.O_CREAT | os.O_WRONLY | os.O_BINARY | os.O_APPEND)
    start = time.perf_counter()
    samples, i = [], 0
    while i / RATE < SECONDS:
        while time.perf_counter() - start < i / RATE:
            time.sleep(0)
        t0 = time.perf_counter()
        os.write(fd, PAYLOAD)
        a = time.perf_counter()
        os.fsync(fd)
        b = time.perf_counter()
        samples.append((t0 - start, (b - a) * 1e3))
        i += 1
    os.close(fd)
    os.remove(path)
    slow = [[round(s, 3), round(ms, 2)] for s, ms in samples if ms >= 3]
    d = sorted(ms for _, ms in samples)
    json.dump({"n": len(d), "p50": d[len(d) // 2], "p99": d[int(len(d) * .99)], "max": d[-1], "slow": slow}, open(os.path.join(OUT, f"L_bare_t{t:02d}.json"), "w"))
    print("bare", t, "max", round(d[-1], 1), "slow", len(slow), flush=True)


for t, arm in enumerate(("A", "AMIN", "B"), 1):
    engine(arm, t)
    bare(t)
