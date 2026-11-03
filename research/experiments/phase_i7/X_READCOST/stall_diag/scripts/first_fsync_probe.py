"""Non-campaign probe: is the first fsync on a recently created file the slow one?

The engine rotates to a new WAL at the last fixture flush, then reads for 2 s
before the first measured commit. Reproduce that with no engine: create a file,
wait 2 s, then append+fsync at the paced rate. Compare writing the first record
straight after creation, and syncing the empty file before the wait.
"""
import os, sys, tempfile, time

ROOT = sys.argv[1]
DIR = tempfile.mkdtemp(prefix="firstfsync", dir=ROOT)
PAYLOAD = b"x" * 140
RATE, SECONDS = 183, 4.0


def run(label, wait_s, presync):
    path = os.path.join(DIR, f"{label}_{time.time_ns()}.wal")
    fd = os.open(path, os.O_CREAT | os.O_WRONLY | os.O_BINARY | os.O_APPEND)
    if presync:
        os.fsync(fd)
    time.sleep(wait_s)
    start = time.perf_counter()
    samples, i = [], 0
    while i / RATE < SECONDS:
        while time.perf_counter() - start < i / RATE:
            time.sleep(0)
        t0 = time.perf_counter()
        os.write(fd, PAYLOAD)
        os.fsync(fd)
        samples.append((time.perf_counter() - t0) * 1e3)
        i += 1
    os.close(fd)
    os.remove(path)
    rest = sorted(samples[1:])
    return samples[0], rest[len(rest) // 2], rest[-1], samples.index(max(samples))


print(f"{'condition':<24}{'first ms':>10}{'later p50':>11}{'later max':>11}{'argmax':>8}")
for rep in range(6):
    for label, wait, presync in (("new+2s", 2.0, False), ("new+0s", 0.0, False), ("new+presync+2s", 2.0, True)):
        f, p50, mx, at = run(label, wait, presync)
        print(f"{label:<24}{f:>10.2f}{p50:>11.2f}{mx:>11.2f}{at:>8}")
os.rmdir(DIR)
