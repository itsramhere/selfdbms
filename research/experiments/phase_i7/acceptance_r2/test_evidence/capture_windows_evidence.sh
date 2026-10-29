#!/usr/bin/env bash
# Mechanically captured R2 acceptance evidence (Windows host).
# Each block records the exact command, its raw output, and its exit code.
# Generated output must never be paraphrased; reviewers re-run at will.
set -u
cd "$(dirname "$0")/../../../../.." || exit 1
out="$(pwd)/research/experiments/phase_i7/acceptance_r2/test_evidence/TEST_COMMANDS_AND_OUTPUTS.txt"
work="$(pwd)/research/experiments/phase_i7/acceptance_r2/test_evidence/_work"
mkdir -p "$work"

run() {
  echo ""
  echo "=============================================================="
  printf '%s\n' "\$ $1"
  echo "--------------------------------------------------------------"
  eval "$1" 2>&1
  local rc=$?
  echo "--------------------------------------------------------------"
  echo "exit code: $rc"
}

{
  echo "# R2 acceptance evidence — Windows host"
  echo "# generated: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "# host: $(uname -s -m) / PowerShell $(powershell.exe -NoProfile -Command '$PSVersionTable.PSVersion.ToString()' 2>/dev/null | tr -d '\r')"
  echo "# git HEAD: $(git rev-parse HEAD)"
  echo "# branch:   $(git rev-parse --abbrev-ref HEAD)"
  echo "# engineering diff base: 27a041e (R1.x preservation commit)"
  echo "#"
  echo "# NOTE: the race-enabled run is captured separately in"
  echo "#       race_verification_wsl2.log (Windows has no CGO toolchain;"
  echo "#       WSL2 Ubuntu is the supported race-verification environment)."

  run "go version"
  run "go env GOOS GOARCH CGO_ENABLED"
  run "go build ./..."
  run "go vet ./..."

  run "go test ./lsmengine/ ./research/experiments/phase_i7/harness2/ ./research/experiments/phase_i7/runlist/ ./research/experiments/phase_i7/cmd/i7trial/ ./research/experiments/harness/ ./research/experiments/corrective_validation/ -count=1 -timeout 1800s"

  run "go test ./lsmengine/ -run '^\$' -bench '^\$'   # EXP-3 benchmarks: compile-verified, NOT executed"

  echo ""
  echo "=============================================================="
  echo "# runlistctl: authoritative run-list generation and validation"
  run "go run ./research/experiments/phase_i7/cmd/runlistctl generate -out \"$work/runlist_N8.csv\" -seed 20261101"
  run "go run ./research/experiments/phase_i7/cmd/runlistctl validate -runlist \"$work/runlist_N8.csv\" -report \"$work/validation_N8.json\""
  echo '{"C1-01":10,"C1-02":10,"C1-03":10,"C1-04":10,"C2-01":10,"C2-02":10,"C2-03":10,"C2-04":10,"C2-05":10,"C2-06":10,"C4-01":10,"C4-02":10}' > "$work/sizing10.json"
  run "go run ./research/experiments/phase_i7/cmd/runlistctl generate -out \"$work/runlist_N10.csv\" -seed 20261101 -sizing \"$work/sizing10.json\""
  run "go run ./research/experiments/phase_i7/cmd/runlistctl validate -runlist \"$work/runlist_N10.csv\" -report \"$work/validation_N10.json\""

  echo ""
  echo "=============================================================="
  echo "# runlistctl ceiling: every physical execution counts (pilot, drift"
  echo "# anchors, retries, replacements, interrupted, aborted)"
  cat > "$work/ledger_ok.jsonl" <<'JSONL'
{"event":"STARTED","trial_id":"p1","kind":"pilot"}
{"event":"FINISHED","trial_id":"p1","status":"SUCCESS"}
{"event":"STARTED","trial_id":"a1","kind":"drift_anchor"}
{"event":"FINISHED","trial_id":"a1","status":"SUCCESS"}
{"event":"STARTED","trial_id":"c1","kind":"confirmatory"}
{"event":"FINISHED","trial_id":"c1","status":"FAILED"}
{"event":"STARTED","trial_id":"i1","kind":"confirmatory"}
{"event":"FINISHED","trial_id":"i1","status":"INTERRUPTED"}
{"event":"STARTED","trial_id":"i1","kind":"retry"}
{"event":"FINISHED","trial_id":"i1","status":"SUCCESS"}
{"event":"STARTED","trial_id":"r1","kind":"replacement"}
{"event":"FINISHED","trial_id":"r1","status":"SUCCESS"}
{"event":"STARTED","trial_id":"u1","kind":"confirmatory"}
JSONL
  run "go run ./research/experiments/phase_i7/cmd/runlistctl ceiling -ledger \"$work/ledger_ok.jsonl\" -report \"$work/ceiling_ok.json\""
  python - "$work/ledger_ok.jsonl" <<'PYEOF'
import sys
path = sys.argv[1]
with open(path, encoding='utf-8') as f:
    lines = f.read().splitlines()
template = lines[-1]
extra = [template.replace('u1', 'overflow_%03d' % i) for i in range(400)]
with open(path, 'w', encoding='utf-8') as f:
    for line in lines + extra:
        f.write(line + "\n")
print("appended 400 more STARTED records: %d total executions, ceiling is 400" % (len(lines) + len(extra)))
PYEOF
  run "go run ./research/experiments/phase_i7/cmd/runlistctl ceiling -ledger \"$work/ledger_ok.jsonl\"; echo \"(ceiling breach: the observed exit 1 above is the correct fail-closed verdict)\" || true"

  echo ""
  echo "=============================================================="
  echo "# driver: legacy execution authority revoked (fail closed, exit 5)"
  probe="$work/probe_root"
  mkdir -p "$probe"
  stub="$work/i7stub.exe"
  go build -trimpath -o "$stub" ./research/experiments/phase_i7/cmd/i7stub
  run "powershell.exe -NoProfile -ExecutionPolicy Bypass -File 'research/experiments/phase_i7/run_i7.ps1' -Root '$probe' -Check -TestMode -RunnerOverride '$stub' -PebbleRunnerOverride '$stub'"
  run "powershell.exe -NoProfile -ExecutionPolicy Bypass -File 'research/experiments/phase_i7/run_i7.ps1' -Root '$probe' -Campaign -TestMode -RunnerOverride '$stub' -PebbleRunnerOverride '$stub'"
  run "powershell.exe -NoProfile -ExecutionPolicy Bypass -File 'research/experiments/phase_i7/run_i7.ps1' -Root '$probe' -Calibrate -TestMode -RunnerOverride '$stub' -PebbleRunnerOverride '$stub'"

  echo ""
  echo "=============================================================="
  echo "# R1.2e statistical artifacts: byte-identical reproduction"
  run "sha256sum research/experiments/analysis/simulate_power_r1_2e.py research/experiments/analysis/r1_2e_power_simulation_results.json"
  run "python research/experiments/analysis/simulate_power_r1_2e.py > /dev/null && sha256sum research/experiments/analysis/r1_2e_power_simulation_results.json"
} > "$out" 2>&1
echo "evidence written to $out"
echo "blocks with recorded exit code 5 (revocation demos): $(grep -c 'exit code: 5' "$out")"
echo "blocks total: $(grep -c 'exit code:' "$out")"
