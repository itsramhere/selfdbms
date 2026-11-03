"""Non-campaign probe: does a plain append+fsync loop, with no engine and no readers, show the ~55 ms stall?"""
import os, tempfile, time

DIR = tempfile.mkdtemp(prefix="fsyncprobe", dir=os.path.dirname(os.path.abspath(__file__)))
PAYLOAD = b"x" * 140
for rate in (183, 732):
    for run in range(3):
        path = os.path.join(DIR, f"p{rate}_{run}.wal")
        fd = os.open(path, os.O_CREAT | os.O_WRONLY | os.O_BINARY)
        period = 1.0 / rate
        start = time.perf_counter()
        samples = []
        i = 0
        while True:
            target = i * period
            if target >= 10.0:
                break
            while time.perf_counter() - start < target:
                time.sleep(0)
            t0 = time.perf_counter()
            os.write(fd, PAYLOAD)
            os.fsync(fd)
            samples.append((t0 - start, (time.perf_counter() - t0) * 1e3, i))
            i += 1
        os.close(fd)
        d = sorted(s[1] for s in samples)
        top = sorted(samples, key=lambda s: -s[1])[:5]
        print(f"rate {rate} run {run}: n {len(d)} p50 {d[len(d)//2]:.2f} p99 {d[int(len(d)*.99)]:.2f} max {d[-1]:.1f} ms; "
              + "top " + ", ".join(f"{s[1]:.1f}ms@{s[0]:.2f}s(#{s[2]})" for s in top))
for f in os.listdir(DIR):
    os.remove(os.path.join(DIR, f))
os.rmdir(DIR)
