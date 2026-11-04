"""Two non-campaign fsync checks. Only stalldiag is launched as a child."""
import argparse
import ctypes
from datetime import datetime, timezone
import json
import math
import os
from pathlib import Path
import random
import subprocess
import time

X = Path(__file__).resolve().parents[2]


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + '\n', encoding='utf-8')


def iso():
    return datetime.now(timezone.utc).isoformat()


class PowerStatus(ctypes.Structure):
    _fields_ = [('ACLineStatus', ctypes.c_ubyte), ('BatteryFlag', ctypes.c_ubyte),
                ('BatteryLifePercent', ctypes.c_ubyte), ('SystemStatusFlag', ctypes.c_ubyte),
                ('BatteryLifeTime', ctypes.c_uint32), ('BatteryFullLifeTime', ctypes.c_uint32)]


def ac_line(kernel):
    status = PowerStatus()
    if not kernel.GetSystemPowerStatus(ctypes.byref(status)):
        raise OSError('GetSystemPowerStatus failed')
    return status.ACLineStatus


def percentile(values, q):
    position = (len(values) - 1) * q
    lo, hi = math.floor(position), math.ceil(position)
    return values[lo] + (values[hi] - values[lo]) * (position - lo)


def bare(out, entry, window, ac):
    rate, block = entry['rate'], entry['block']
    path = out / 'data' / f'r{rate:04d}_b{block:02d}.wal'
    if path.exists():
        raise FileExistsError(path)
    fd = os.open(path, os.O_CREAT | os.O_WRONLY | os.O_BINARY | os.O_APPEND)
    samples = []
    try:
        time.sleep(2)
        start_iso = iso()
        start = time.perf_counter()
        i = 0
        while i / rate < window:
            while time.perf_counter() - start < i / rate:
                time.sleep(0)
            os.write(fd, b'x' * 140)
            a = time.perf_counter()
            os.fsync(fd)
            b = time.perf_counter()
            samples.append((a - start, (b - a) * 1000))
            i += 1
        finished = time.perf_counter()
        end_iso = iso()
    finally:
        os.close(fd)
        path.unlink()
    values = sorted(ms for _, ms in samples)
    record = dict(entry, start_iso=start_iso, end_iso=end_iso, ac_line=ac,
                  n=len(values), window_s=window, fsync_p50_ms=percentile(values, .5),
                  fsync_p99_ms=percentile(values, .99), fsync_max_ms=values[-1],
                  first_fsync_ms=samples[0][1], count_ge_25=sum(ms >= 25 for ms in values),
                  sum_ge_25_ms=sum(ms for ms in values if ms >= 25),
                  slow=[[round(t, 3), round(ms, 3)] for t, ms in samples if ms >= 3])
    write_json(out / 'bare' / f'r{rate:04d}_b{block:02d}.json', record)
    return record, finished


def engine(exe, out, entry, ac):
    tid = entry['id']
    data = out / 'data' / tid
    data.mkdir(exist_ok=True)
    if any(data.iterdir()):
        raise RuntimeError(f'Data directory must be empty: {data}')
    spec = X / 'settled' / 'specs' / f"S3_d0000_f80_closed_t{entry['round']:02d}_{entry['arm']}_a1.json"
    record = dict(entry, start_iso=iso(), ac_line=ac)
    try:
        run = subprocess.run([str(exe), '-spec', str(spec), '-out', str(out / 'engine' / (tid + '.json')),
                              '-data-dir', str(data), '-wal-sync', str(entry['condition'] == 'on').lower(),
                              '-trial-id', tid], stderr=subprocess.PIPE, text=True)
        record.update(exit_code=run.returncode, stderr=run.stderr)
    except OSError as error:
        record.update(exit_code=None, stderr=str(error))
    finished = time.perf_counter()
    record['end_iso'] = iso()
    write_json(out / 'engine' / (tid + '.meta.json'), record)
    return record, finished


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--exe', type=Path, required=True)
    parser.add_argument('--out', type=Path, default=X / 'fsync_checks')
    parser.add_argument('--smoke', action='store_true')
    args = parser.parse_args()
    out, exe = args.out.resolve(), args.exe.resolve()
    for name in ('bare', 'engine'):
        if (out / name).exists() and any((out / name).rglob('*.json')):
            parser.error(f'Refusing to overwrite existing result files in {out / name}')
    if not exe.is_file():
        parser.error(f'Executable does not exist: {exe}')
    if os.name != 'nt':
        parser.error('These checks require Windows power status APIs')
    kernel = ctypes.WinDLL('kernel32', use_last_error=True)
    if ac_line(kernel) != 1:
        parser.error('AC power is required to start (ACLineStatus must be 1)')
    if not kernel.SetThreadExecutionState(0x80000000 | 0x00000001):
        raise OSError('SetThreadExecutionState failed')
    try:
        for name in ('bare', 'engine', 'data'):
            (out / name).mkdir(parents=True, exist_ok=True)
        rng, entries = random.Random(20261103), []
        for block in range(1, 2 if args.smoke else 11):
            rates = [183] if args.smoke else [72, 155, 183, 700]
            rng.shuffle(rates)
            entries.extend(dict(part='bare', rate=rate, block=block) for rate in rates)
        for rnd in range(1, 2 if args.smoke else 11):
            trials = [(condition, arm) for condition in ('on', 'off')
                      for arm in (('AMIN',) if args.smoke else ('AMIN', 'B'))]
            rng.shuffle(trials)
            entries.extend(dict(part='engine', condition=c, arm=a, round=rnd,
                                id=f'F_{c}_{a}_t{rnd:02d}') for c, a in trials)
        for position, entry in enumerate(entries, 1):
            entry['order_position'] = position
        pause, window = (3, 3) if args.smoke else (20, 10)
        write_json(out / 'plan.json', dict(seed=20261103, smoke=args.smoke,
                                         pause_s=pause, window_s=window, runs=entries))
        finished = time.perf_counter()
        for entry in entries:
            time.sleep(max(0, finished + pause - time.perf_counter()))
            ac = ac_line(kernel)
            if entry['part'] == 'bare':
                record, finished = bare(out, entry, window, ac)
            else:
                record, finished = engine(exe, out, entry, ac)
            with (out / 'progress.jsonl').open('a', encoding='utf-8') as stream:
                stream.write(json.dumps(record, sort_keys=True) + '\n')
            print(f"{entry['order_position']}/{len(entries)} {entry.get('id', 'bare r' + str(entry.get('rate', '')))} ac={ac} exit={record.get('exit_code', 0)}", flush=True)
    finally:
        kernel.SetThreadExecutionState(0x80000000)


if __name__ == '__main__':
    main()
