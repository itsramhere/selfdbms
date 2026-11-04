# X-inputopen-v1 implementation report

Implemented on `research/x-phase3`. No commit, campaign trial, smoke, or race run
was performed. Unit tests, synthetic fixtures, runner self-test and driver dry-run
are the only verification observations. The protocol remains PRE-DATA and is not
represented as committed/frozen.

## Changed files

- `lsmengine/manager.go`: default-false experiment config, stored flag and test opener.
- `lsmengine/compaction.go`: release the start hold after the name copy only for the aligned path; preserve default lock placement, original errors, cleanup of prior iterators and merge-hook position. Snapshot compaction is unchanged.
- `lsmengine/compaction_inputopen_test.go`: A/AMIN default/aligned hold and instrumentation checks, merge/read correctness, flushes while opening, open-error identity and cleanup, manager reuse, held Lease reader through retirement, Snapshot flag isolation, and concurrent reader stress in both modes and paths.
- `research/experiments/phase_i7/harness2/spec.go`: omitted-when-false JSON field, restricted to X blocks.
- `research/experiments/phase_i7/harness2/run.go`: pass the flag to engine config.
- `research/experiments/phase_i7/harness2/inputopen_test.go`: X-block restriction and omitted default field checks.
- `research/experiments/phase_i7/cmd/xcampaign/inputopen.go`: six settings, five runs each, ten rounds, master seed 20261110, independent registered permutations, paired entry seeds and 20000 ms settling.
- `research/experiments/phase_i7/cmd/xcampaign/inputopen_test.go`: inherited compaction-on fixtures, 6 entries/12 cells/300 unique trials, B only under default, seed/order/position/determinism and new frozen plan hash.
- `research/experiments/phase_i7/cmd/xcampaign/main.go`: design selection, default seed, X3 preflight requirement and three-hour budget.
- `research/experiments/phase_i7/cmd/xcampaign/followup_test.go`: extend the frozen earlier-plan checks with X-locks-v1. All eight earlier plan hashes, including followup and locks, remain unchanged.
- `research/experiments/phase_i7/cmd/i7trial/main.go`: approve X-inputopen-v1 only for X3.
- `research/experiments/phase_i7/cmd/i7trial/main_test.go`: accept X3 and reject X1/X2/X4/ANCHOR/E1 for this campaign.
- `research/experiments/analysis/x_inputopen_analyze.py`: all H-O1..H-O4 tests and descriptive reporting, explicit tail/throughput margins, separate Holm families, paired bootstrap sensitivity, drift, provenance, failures/whole-unit exclusions, zero rules, deterministic CLI and one figure. Earlier analyzers are unchanged.
- `research/experiments/analysis/test_x_inputopen_analyze.py`: 12 synthetic checks covering predictions, descriptive outputs, known tail-margin TOST, Holm missing-test membership, zero/undefined rules, partial data, exhausted retries/latest attempts, smoke exclusion, plan/provenance faults and byte-identical JSON/Markdown/PNG outputs.
- `research/experiments/phase_i7/X_INPUTOPEN_PREREGISTRATION.md`: copied verbatim from the supplied scratchpad protocol, then changed only the specifically requested hash line. A byte comparison against the source after reversing that one substitution passed.
- `research/experiments/phase_i7/PHASE3_RUN_INSTRUCTIONS.md`: preparation, freeze and later operator-approved execution, approximately three-hour budget, approval binding, analysis and disclosed limitation.
- `research/experiments/phase_i7/cmd/xcampaign/README.md`: inputopen design paragraph.
- `research/experiments/phase_i7/X_INPUTOPEN_IMPLEMENTATION_REPORT.md`: this report.

## Plan and verification

Plan SHA-256 (indented JSON bytes without trailing newline):
`5b685feb2327ff8d9b1e72b4bc0637570f0fe0014690ec9e214e84f163893a98`.

All Go commands used `$env:GODEBUG = 'goindex=0'`.

- `go test ./lsmengine/ ./research/experiments/phase_i7/... -count=1`: initial verification found a Windows-specific closed-file assertion issue and a pre-existing rapid-compaction filename collision. The assertion now uses ReadAt, and the stress fixture advances past the previous output-name tick. Subsequent full runs passed engine/driver/gate/hash tests but exposed intermittent pre-existing 500 ms E2 timing assertions in harness2: `TestRunPublicationOnlyDriver` and `TestE2_PhysicalVsNoneModeComparison` each observed 1 in-window event and 1 late event instead of 2 in-window events. Those tests and harness behavior were left unchanged. The final run of the exact requested command passed every package (engine 16.469 s, harness2 12.914 s, driver 2.445 s).
- `go test ./lsmengine/ ./research/experiments/phase_i7/... -count=1 -p=1`: PASS for every package. This serializes package execution to reduce timing-test contention.
- `python -m unittest discover -s research/experiments/analysis -p "test_x_inputopen*.py"`: PASS, 12 tests. An early synthetic-fixture run needed the repository-local temporary-directory pattern because the sandbox denied access beneath Python-created OS temporary directories; an exact floating-point equality assertion was also corrected. The final run passed in 18.818 s.
- `go build -o <scratchpad>/inputopen-verification/i7trial.exe ./research/experiments/phase_i7/cmd/i7trial`, followed by `go run ./research/experiments/phase_i7/cmd/xcampaign -design inputopen -runner <that executable> -out <temporary output directory> -dry-run -allow-dirty`: preflight PASS, 6 settings, 12 cells, 300 trials, X3, 20000 ms pause and nominal ETA 2h42m30s. Dry-run performs the runner's platform self-test and launches no trial.
- Final dry-run directory: `C:/Users/ramst/AppData/Local/Temp/claude/8a41a9e007ad83fc/c4daef8c-329e-45ab-9d62-3e83c9298e35/scratchpad/inputopen-verification/dry-run-final`. Final runner SHA-256: `3425e6ef247413708ebbd14e8794d41f83aeaf8a753855328b7c01cd814c3747`. The final preflight passed with `allow_dirty=true`; no execution approval was created.

The actual generated plan passed `x_inputopen_analyze.validate_plan`: 300 identities, 60 complete five-run units, correct default seed and hash. The dry-run output contains no trial artifacts.
- A comparison against `git show HEAD:lsmengine/compaction.go` confirmed that `compactAllApproachBObserved` and the remaining Snapshot compaction source are unchanged.
- `git diff --check`: PASS.
- Race verification is intentionally left to the operator.

## Retirement audit, conflicts and limits

No internal manager path can retire an A/AMIN input between copying its name and
opening its iterator: CompactAll holds compactionMu for its entire lifetime;
other compactions and A/AMIN Close acquire that same mutex. Flush publication
only appends readers and cannot retire the copied active prefix. Lease release
can reclaim only already-retired readers, which are disjoint from that prefix.
Snapshot follows its unchanged separate reference/lifetime path. Direct external
closure/removal through exposed file objects or the filesystem is outside these
manager invariants.

No protocol treatment, cell, seed, margin or prediction was changed. A pre-existing
engine limitation prevents an unrestricted claim that arbitrary back-to-back
compactions are safe on this host: output paths use
`data_compacted_<time.Now().UnixNano()>.sst`. Two compactions in one Windows clock
tick can reuse an active input pathname; os.Create then truncates that input.
The new stress test initially reproduced exactly that pathname reuse, concurrent
read EOFs and a Windows removal sharing error. This occurs later in the merge,
not in the name-copy/open interval. Output naming was deliberately left unchanged
under the instruction to report conflicts instead of altering the design. The
stress fixture waits until the previous output's clock tick has passed before
starting another compaction and retains an assertion against pathname reuse.
The registered campaign still uses its specified 250 ms timer; no campaign
observation or smoke was used to assess the limitation.

The unchanged iterator constructor can also leave its own newly opened handle
unclosed if metadata parsing fails after os.Open; this implementation guarantees
closure of previously returned iterators on any opening error, as requested.
The deterministic error test injects a second-open error and verifies exact error
identity, first-iterator closure and subsequent manager use. Corrupt-input
constructor resource cleanup remains outside this treatment.

Race behavior has not been verified in this implementation session. The existing
E2 wall-clock test sensitivity and the rapid-compaction filename collision remain
reported limitations; neither was fixed by changing earlier behavior.
