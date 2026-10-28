# Phase I7 — G3 Calibration Corrective Packet

**DECISION / STATUS:** G3 Preflight Accepted (12/12 SUCCESS). Corrective patch applied. **STOP BEFORE CALIBRATION. G4/G5 REMAIN UNAUTHORIZED.**  
**Date:** 2026-10-28  
**Host:** Windows 11 Home 10.0.26200 · AMD Ryzen 7 7445HS (6C/12T) · 15.2 GiB · Samsung MZVL8512HFLU-00BH1 NVMe · NTFS  
**Toolchain:** `go1.26.3 windows/amd64`, `CGO_ENABLED=0`  
**Protocol Version:** `1.2.0-windows`  
**Accepted G3 Preflight Archive:** `research/experiments/phase_i7/I7_WINDOWS_RESULTS/I7_WINDOWS_RESULTS.zip`  
**Verified SHA-256:** `323b5e5cff58cf4a868f3e68435d1d980cc83d1d93fc403ea47a4c2bf8a48a4e` (preserved immutably)  
**Active Commit:** `caefc7b` (`fix(i7-g3): calibration balanced Latin-square seeds, strict completeness, and E2 publication window timing`)  
**Parent / Preflight Commit:** `4c7195e`  
**Working Tree Status:** Clean (`git_dirty = false`)  

---

## 1. Summary of Required Corrections & Implementation

### 1. Calibration Seeds & Balanced Order
- **Shared Request-Stream Seeds**: In `Invoke-Calibrate`, seeds are now anchored per triplet:
  $$\text{Seed}(r) = \text{uint64}\left(\text{SHA256}(\text{"cal|triplet|" + } r)[0:15]\right)$$
  Within each repetition $r \in \{1, 2, 3\}$, arms A, A-min, and B receive the exact same logical key stream.
- **Balanced Latin-Square Execution Order**:
  Instead of sequential blocks (all A, then all A-min, then all B), trials rotate in a 3x3 Latin square across the 3 repetitions:
  * Triplet 1 ($r=1$): Order 1 = `A`, Order 2 = `AMIN`, Order 3 = `B`
  * Triplet 2 ($r=2$): Order 4 = `AMIN`, Order 5 = `B`, Order 6 = `A`
  * Triplet 3 ($r=3$): Order 7 = `B`, Order 8 = `A`, Order 9 = `AMIN`
  Each arm appears exactly once in position 1, 2, and 3, balancing temporal drift and thermal progression.

### 2. Strict Calibration Completeness
- `Invoke-Calibrate` enforces that all 9 planned cells must have valid successful trial results.
- If any cell is missing or failed (even 8/9 successes), `Invoke-Calibrate` throws an explicit error enumerating missing cells and refuses to produce `calibration.json` or calculate $C_{\text{ref}}$.
- When all 9 cells are present, each arm's median is computed strictly from its 3 planned repetitions:
  $$\text{Median}_a = \text{sorted}(v_{a,1}, v_{a,2}, v_{a,3})[1]$$
  $$C_{\text{ref}} = \min(\text{Median}_A, \text{Median}_{A\text{-min}}, \text{Median}_B)$$

### 3. E2 Publication Window Timing & Late Event Partitioning
- In `harness2/events.go`, `EventRecorder.Events(windowNs)` partitions publications into:
  - In-Window: $\text{EndNs} \le \text{windowNs}$ (`InWindow = true`, recorded in `TrialResult.Events`)
  - Late: $\text{EndNs} > \text{windowNs}$ (`InWindow = false`, recorded in `TrialResult.EventsLate`)
- In `harness2/spec.go`, `Diagnostics` records:
  - `PublicationEventsRealizedInWindow`
  - `PublicationEventsRealizedLate`
- In `harness2/run.go`:
  In physical publication mode (`isPhysical`), if `PublicationEventsRealizedInWindow != targetEvents`, the trial fails closed with an explicit timing violation error (`"E2 publication timing: expected N in-window realized events, got X (Y late)"`), preventing late flushes from masking publication critical-section contention.

---

## 2. Source Diff

```diff
diff --git a/research/experiments/phase_i7/harness2/events.go b/research/experiments/phase_i7/harness2/events.go
index af87bad..4ec5485 100644
--- a/research/experiments/phase_i7/harness2/events.go
+++ b/research/experiments/phase_i7/harness2/events.go
@@ -101,13 +101,20 @@ func (r *EventRecorder) OnFlushPublishEnd(err error) {
 // OnFlushComplete implements lsmengine.FlushObserver.
 func (r *EventRecorder) OnFlushComplete(err error) {}
 
-// Events returns a copy of the recorded events.
-func (r *EventRecorder) Events() []EventRecord {
+// Events partitions recorded events into in-window (EndNs <= windowNs) and late (EndNs > windowNs).
+func (r *EventRecorder) Events(windowNs int64) (inWindow []EventRecord, late []EventRecord) {
 	r.mu.Lock()
 	defer r.mu.Unlock()
-	out := make([]EventRecord, len(r.events))
-	copy(out, r.events)
-	return out
+	for _, e := range r.events {
+		if windowNs <= 0 || e.EndNs <= windowNs {
+			e.InWindow = true
+			inWindow = append(inWindow, e)
+		} else {
+			e.InWindow = false
+			late = append(late, e)
+		}
+	}
+	return inWindow, late
 }
 
 // ConditionEvents selects reads that overlap each publication window (± delta)
diff --git a/research/experiments/phase_i7/harness2/run.go b/research/experiments/phase_i7/harness2/run.go
index 80e9215..4ede1a2 100644
--- a/research/experiments/phase_i7/harness2/run.go
+++ b/research/experiments/phase_i7/harness2/run.go
@@ -381,8 +381,16 @@ func Run(spec Spec, env Environment, gitRev string) (*TrialResult, error) {
 	}
 
 	if rec != nil {
-		res.Events = rec.Events()
+		inWin, lateEvents := rec.Events(windowNs)
+		res.Events = inWin
+		res.EventsLate = lateEvents
 		res.EventCond = ConditionEvents(res.Events, MergeIssues(shards), eventDeltaNs)
+		res.LSMDiagnostics.PublicationEventsRealizedInWindow = len(inWin)
+		res.LSMDiagnostics.PublicationEventsRealizedLate = len(lateEvents)
+		if isPhysical && len(inWin) != targetEvents && res.Error == "" {
+			res.Status = "FAILED"
+			res.Error = fmt.Sprintf("E2 publication timing: expected %d in-window realized events, got %d (%d late)", targetEvents, len(inWin), len(lateEvents))
+		}
 	}
 
 	// Shutdown. A non-nil error is a failed trial: for A-min a lease drain
diff --git a/research/experiments/phase_i7/harness2/run_test.go b/research/experiments/phase_i7/harness2/run_test.go
index d56906a..fa63d32 100644
--- a/research/experiments/phase_i7/harness2/run_test.go
+++ b/research/experiments/phase_i7/harness2/run_test.go
@@ -431,6 +431,15 @@ func TestE2_PhysicalVsNoneModeComparison(t *testing.T) {
 	if len(resPhys.Events) != 4 {
 		t.Fatalf("physical event log expected 4 records, got %d", len(resPhys.Events))
 	}
+	if diagPhys.PublicationEventsRealizedInWindow != 4 {
+		t.Fatalf("physical realized in-window expected 4, got %d", diagPhys.PublicationEventsRealizedInWindow)
+	}
+	if diagPhys.PublicationEventsRealizedLate != 0 {
+		t.Fatalf("physical realized late expected 0, got %d", diagPhys.PublicationEventsRealizedLate)
+	}
+	if len(resPhys.EventsLate) != 0 {
+		t.Fatalf("physical late events expected 0, got %d", len(resPhys.EventsLate))
+	}
 	if resPhys.Operations.WriteOpsAttempted != 0 {
 		t.Fatalf("physical writes attempted expected 0, got %d", resPhys.Operations.WriteOpsAttempted)
 	}
@@ -460,6 +469,15 @@ func TestE2_PhysicalVsNoneModeComparison(t *testing.T) {
 	if diagNone.PublicationEventsRealized != 0 {
 		t.Fatalf("none realized events expected 0, got %d", diagNone.PublicationEventsRealized)
 	}
+	if diagNone.PublicationEventsRealizedInWindow != 0 {
+		t.Fatalf("none realized in-window expected 0, got %d", diagNone.PublicationEventsRealizedInWindow)
+	}
+	if diagNone.PublicationEventsRealizedLate != 0 {
+		t.Fatalf("none realized late expected 0, got %d", diagNone.PublicationEventsRealizedLate)
+	}
+	if len(resNone.EventsLate) != 0 {
+		t.Fatalf("none late events expected 0, got %d", len(resNone.EventsLate))
+	}
 	if diagNone.InitialFrozenMemtables != 4 {
 		t.Fatalf("none initial frozen memtables expected 4 (matched workload), got %d", diagNone.InitialFrozenMemtables)
@@ -484,6 +502,30 @@ func TestE2_PhysicalVsNoneModeComparison(t *testing.T) {
 	}
 }
 
+func TestE2_EventRecorderWindowPartitioning(t *testing.T) {
+	base := Now()
+	rec := NewEventRecorder(base)
+	rec.events = []EventRecord{
+		{Index: 0, BeginNs: 100, EndNs: 500},
+		{Index: 1, BeginNs: 600, EndNs: 1000},
+		{Index: 2, BeginNs: 900, EndNs: 1200},
+	}
+	inWin, late := rec.Events(1000)
+	if len(inWin) != 2 {
+		t.Fatalf("expected 2 in-window events, got %d", len(inWin))
+	}
+	if !inWin[0].InWindow || !inWin[1].InWindow {
+		t.Fatalf("expected InWindow=true for in-window events")
+	}
+	if len(late) != 1 {
+		t.Fatalf("expected 1 late event, got %d", len(late))
+	}
+	if late[0].InWindow {
+		t.Fatalf("expected InWindow=false for late event")
+	}
+	if late[0].Index != 2 {
+		t.Fatalf("expected late event index 2, got %d", late[0].Index)
+	}
+}
+
 // TestE4_OpenLoopOverloadAndLateAccounting verifies:
diff --git a/research/experiments/phase_i7/harness2/spec.go b/research/experiments/phase_i7/harness2/spec.go
index bce416d..686315c 100644
--- a/research/experiments/phase_i7/harness2/spec.go
+++ b/research/experiments/phase_i7/harness2/spec.go
@@ -171,13 +171,15 @@ type ThroughputInfo struct {
 // Diagnostics carries engine activity plus the accounting needed to keep
 // per-trial work visible (audit AF-4: composition shifts must not be hidden).
 type Diagnostics struct {
-	InitialSSTables           int    `json:"initial_sstables"`
-	FinalSSTables             int    `json:"final_sstables"`
-	InitialFrozenMemtables    int    `json:"initial_frozen_memtables"`
-	FinalFrozenMemtables      int    `json:"final_frozen_memtables"`
-	PublicationEventsTarget   int    `json:"publication_events_target"`
-	PublicationEventsReleased int    `json:"publication_events_released"`
-	PublicationEventsRealized uint64 `json:"publication_events_realized"`
+	InitialSSTables                   int    `json:"initial_sstables"`
+	FinalSSTables                     int    `json:"final_sstables"`
+	InitialFrozenMemtables            int    `json:"initial_frozen_memtables"`
+	FinalFrozenMemtables              int    `json:"final_frozen_memtables"`
+	PublicationEventsTarget           int    `json:"publication_events_target"`
+	PublicationEventsReleased         int    `json:"publication_events_released"`
+	PublicationEventsRealizedInWindow int    `json:"publication_events_realized_in_window"`
+	PublicationEventsRealizedLate     int    `json:"publication_events_realized_late"`
+	PublicationEventsRealized         uint64 `json:"publication_events_realized"`
 	TopologyNote              string `json:"topology_note,omitempty"`
 	MeasuredFlushes           uint64 `json:"measured_flushes"`
@@ -206,6 +208,7 @@ type EventRecord struct {
 	HoldNs       int64  `json:"hold_ns"`
 	BuildNs      int64  `json:"build_ns"`
 	MemtableKeys int    `json:"memtable_keys"`
+	InWindow     bool   `json:"in_window"`
 }
@@ -254,6 +257,7 @@ type TrialResult struct {
 	LSMDiagnostics      Diagnostics           `json:"lsm_diagnostics"`
 	Events          []EventRecord         `json:"publication_events,omitempty"`
+	EventsLate      []EventRecord         `json:"publication_events_late,omitempty"`
 	EventCond       []EventConditioned    `json:"event_conditioned,omitempty"`
 	FixtureManifest harness.FixtureManifest `json:"fixture_manifest"`
diff --git a/research/experiments/phase_i7/run_i7.ps1 b/research/experiments/phase_i7/run_i7.ps1
index c6b96db..dcfb0aa 100644
--- a/research/experiments/phase_i7/run_i7.ps1
+++ b/research/experiments/phase_i7/run_i7.ps1
@@ -1506,39 +1506,45 @@ function Invoke-Calibrate {
+    # Balanced Latin-square rotation across 3 triplets:
+    # Triplet 1: A, AMIN, B
+    # Triplet 2: AMIN, B, A
+    # Triplet 3: B, A, AMIN
+    # All arms in Triplet r share the same logical request-stream seed.
+    $plan = @(
+        [ordered]@{ Arm = 'A';    Rep = 1; Triplet = 1; Order = 1 },
+        [ordered]@{ Arm = 'AMIN'; Rep = 1; Triplet = 1; Order = 2 },
+        [ordered]@{ Arm = 'B';    Rep = 1; Triplet = 1; Order = 3 },
+        [ordered]@{ Arm = 'AMIN'; Rep = 2; Triplet = 2; Order = 4 },
+        [ordered]@{ Arm = 'B';    Rep = 2; Triplet = 2; Order = 5 },
+        [ordered]@{ Arm = 'A';    Rep = 2; Triplet = 2; Order = 6 },
+        [ordered]@{ Arm = 'B';    Rep = 3; Triplet = 3; Order = 7 },
+        [ordered]@{ Arm = 'A';    Rep = 3; Triplet = 3; Order = 8 },
+        [ordered]@{ Arm = 'AMIN'; Rep = 3; Triplet = 3; Order = 9 }
+    )
+
     # Check if all 9 calibration runs are already complete (idempotency)
-    $allSuccessful = $true
     $completedMap = @{}
-    foreach ($arm in 'A', 'AMIN', 'B') {
-        for ($rep = 1; $rep -le 3; $rep++) {
-            $tid = ('cal_{0}_{1:D2}' -f $arm, $rep)
-            $outPath = Join-Path $Layout.Calibration ($tid + '.json')
-            $hasSuccessInLedger = @($finishedSuccess | Where-Object { $_.trial_id -eq $tid }).Count -gt 0
-            if ($hasSuccessInLedger -and (Test-Path -LiteralPath $outPath)) {
-                $j = Read-JsonFile $outPath
-                if ($j -and $j.status -eq 'SUCCESS' -and $j.result -and $j.result.throughput) {
-                    $completedMap[$tid] = $j
-                    continue
-                }
-            }
-            $allSuccessful = $false
-        }
-    }
+    foreach ($item in $plan) {
+        $tid = ('cal_{0}_{1:D2}' -f $item.Arm, $item.Rep)
+        $outPath = Join-Path $Layout.Calibration ($tid + '.json')
+        $hasSuccessInLedger = @($finishedSuccess | Where-Object { $_.trial_id -eq $tid }).Count -gt 0
+        if ($hasSuccessInLedger -and (Test-Path -LiteralPath $outPath)) {
+            $j = Read-JsonFile $outPath
+            if ($j -and $j.status -eq 'SUCCESS' -and $j.result -and $j.result.throughput) {
+                $completedMap[$tid] = $j
+            }
+        }
+    }
+    $allSuccessful = ($completedMap.Count -eq $plan.Count)
 
     if ($allSuccessful) {
         Write-Ok ('calibration already complete (9/9 runs); no trials executed')
-        $perArm = @{}
-        $runs = New-Object System.Collections.ArrayList
-        foreach ($arm in 'A', 'AMIN', 'B') {
-            $vals = New-Object System.Collections.ArrayList
-            for ($rep = 1; $rep -le 3; $rep++) {
-                $tid = ('cal_{0}_{1:D2}' -f $arm, $rep)
-                $j = $completedMap[$tid]
-                [void]$vals.Add([double]$j.result.throughput.read_ops_per_sec)
-                [void]$runs.Add([ordered]@{ trial_id = $tid; arm = $arm; read_ops_per_sec = [double]$j.result.throughput.read_ops_per_sec })
-            }
-            $sorted = @($vals | Sort-Object)
-            $perArm[$arm] = [double]$sorted[[int]($sorted.Count / 2)]
-        }
-        $cRef = ($perArm.Values | Measure-Object -Minimum).Minimum
-        $cal = [ordered]@{
-            schema = 'i7.calibration.v1'; created_utc = Get-UtcStamp; protocol_version = $script:I7.ProtocolVersion
-            per_arm_read_ops_per_sec = $perArm; c_ref = [double]$cRef; rule = 'min over arms of median read ops/s at r=16, 5 s, 3 reps'
-            runs = $runs
-        }
-        Write-JsonFile -Path $Layout.CalibrationOut -Object $cal
-        Write-Ok ('C_ref = {0:N1} read ops/s (min over arms: A={1:N1}, AMIN={2:N1}, B={3:N1})' -f $cRef, $perArm['A'], $perArm['AMIN'], $perArm['B'])
-        return
     }
+    else {
+        if ($DryRun) {
+            Write-Host 'DRY RUN: 9 calibration runs planned under gate G3 (PF block, balanced Latin-square order, r=16, 5s window, 3 reps x 3 arms).' -ForegroundColor Yellow
+            foreach ($item in $plan) {
+                $tid = ('cal_{0}_{1:D2}' -f $item.Arm, $item.Rep)
+                $status = if ($completedMap.ContainsKey($tid)) { 'ALREADY_COMPLETED' } else { 'PLANNED' }
+                Write-Host ('  order={0} {1,-15} arm={2,-5} rep={3} triplet={4} status={5}' -f $item.Order, $tid, $item.Arm, $item.Rep, $item.Triplet, $status)
+            }
+            exit 0
+        }
 
-        # Execute remaining runs, enforcing attempt ceiling (hard ceiling = 12 attempts: 9 planned + 3 replacements)
+        $maxCalibrationAttempts = 12
 
-    foreach ($arm in 'A', 'AMIN', 'B') {
-        $vals = New-Object System.Collections.ArrayList
-        for ($rep = 1; $rep -le 3; $rep++) {
-            $tid = ('cal_{0}_{1:D2}' -f $arm, $rep)
+        foreach ($item in $plan) {
+            $arm = $item.Arm
+            $rep = $item.Rep
+            $tid = ('cal_{0}_{1:D2}' -f $arm, $rep)
             if ($completedMap.ContainsKey($tid)) {
                 $j = $completedMap[$tid]
-                [void]$vals.Add([double]$j.result.throughput.read_ops_per_sec)
-                [void]$runs.Add([ordered]@{ trial_id = $tid; arm = $arm; read_ops_per_sec = [double]$j.result.throughput.read_ops_per_sec })
                 Write-Info ('[cal] {0} already completed ({1:N1} ops/s); skipping' -f $tid, [double]$j.result.throughput.read_ops_per_sec)
                 continue
             }
@@ -1586,8 +1586,9 @@ function Invoke-Calibrate {
+            $seed = [int64]([Convert]::ToUInt64((Get-TextSha256 ('cal|triplet|' + $item.Triplet)).Substring(0, 15), 16))
             $spec = [ordered]@{
-                schema = 'i7.trial-spec.v1'; trial_id = $tid; block = 'PF'; cell = 'calibration'; triplet = $rep
-                arm = $arm; order_position = $rep; runlist_position = 0; seed = [int64]([Convert]::ToUInt64((Get-TextSha256 ('cal|' + $arm + '|' + $rep)).Substring(0, 15), 16))
+                schema = 'i7.trial-spec.v1'; trial_id = $tid; block = 'PF'; cell = 'calibration'; triplet = $item.Triplet
+                arm = $arm; order_position = $item.Order; runlist_position = 0; seed = $seed
@@ -1602,3 +1603,3 @@ function Invoke-Calibrate {
-            Add-JsonLine -Path $ledgerPath -Object ([ordered]@{ event = 'STARTED'; trial_id = $tid; arm = $arm; rep = $rep; ts = Get-UtcStamp })
+            Add-JsonLine -Path $ledgerPath -Object ([ordered]@{ event = 'STARTED'; trial_id = $tid; arm = $arm; rep = $rep; triplet = $item.Triplet; order = $item.Order; ts = Get-UtcStamp })
@@ -1618,3 +1619,3 @@ function Invoke-Calibrate {
-            Add-JsonLine -Path $ledgerPath -Object ([ordered]@{ event = 'FINISHED'; trial_id = $tid; arm = $arm; rep = $rep; status = $status; exit_code = $exitCode; ts = Get-UtcStamp })
+            Add-JsonLine -Path $ledgerPath -Object ([ordered]@{ event = 'FINISHED'; trial_id = $tid; arm = $arm; rep = $rep; triplet = $item.Triplet; order = $item.Order; status = $status; exit_code = $exitCode; ts = Get-UtcStamp })
@@ -1620,4 +1621,3 @@ function Invoke-Calibrate {
             if ($status -eq 'SUCCESS' -and $j.result -and $j.result.throughput) {
-                [void]$vals.Add([double]$j.result.throughput.read_ops_per_sec)
-                [void]$runs.Add([ordered]@{ trial_id = $tid; arm = $arm; read_ops_per_sec = [double]$j.result.throughput.read_ops_per_sec })
+                $completedMap[$tid] = $j
                 Write-Ok ('[cal] {0} SUCCESS ({1:N1} ops/s)' -f $tid, [double]$j.result.throughput.read_ops_per_sec)
@@ -1627,6 +1627,15 @@ function Invoke-Calibrate {
         }
-        if ($vals.Count -gt 0) {
-            $sorted = @($vals | Sort-Object)
-            $perArm[$arm] = [double]$sorted[[int]($sorted.Count / 2)]
-        }
     }
+
+    # Strict completeness: all 9 planned calibration cells must have valid successful records.
+    $missingCells = New-Object System.Collections.ArrayList
+    foreach ($item in $plan) {
+        $tid = ('cal_{0}_{1:D2}' -f $item.Arm, $item.Rep)
+        if (-not $completedMap.ContainsKey($tid)) {
+            [void]$missingCells.Add($tid)
+        }
+    }
+    if ($missingCells.Count -gt 0) {
+        throw ('calibration incomplete: {0}/9 cells succeeded. Missing cells: {1}' -f ($plan.Count - $missingCells.Count), ($missingCells -join ', '))
+    }
+
+    $perArm = @{}
+    $runs = New-Object System.Collections.ArrayList
+    foreach ($arm in 'A', 'AMIN', 'B') {
+        $vals = New-Object System.Collections.ArrayList
+        for ($rep = 1; $rep -le 3; $rep++) {
+            $tid = ('cal_{0}_{1:D2}' -f $arm, $rep)
+            $j = $completedMap[$tid]
+            [void]$vals.Add([double]$j.result.throughput.read_ops_per_sec)
+            [void]$runs.Add([ordered]@{ trial_id = $tid; arm = $arm; rep = $rep; read_ops_per_sec = [double]$j.result.throughput.read_ops_per_sec })
+        }
+        $sorted = @($vals | Sort-Object)
+        $perArm[$arm] = [double]$sorted[1]
+    }
     $cRef = ($perArm.Values | Measure-Object -Minimum).Minimum
     $cal = [ordered]@{
         schema = 'i7.calibration.v1'; created_utc = Get-UtcStamp; protocol_version = $script:I7.ProtocolVersion
-        per_arm_read_ops_per_sec = $perArm; c_ref = [double]$cRef; rule = 'min over arms of median read ops/s at r=16, 5 s, 3 reps'
+        per_arm_read_ops_per_sec = $perArm; c_ref = [double]$cRef; rule = 'min over arms of median read ops/s at r=16, 5 s, 3 reps (balanced Latin-square order)'
         runs = $runs
     }
     Write-JsonFile -Path $Layout.CalibrationOut -Object $cal
diff --git a/research/experiments/phase_i7/tests/orchestration_tests.ps1 b/research/experiments/phase_i7/tests/orchestration_tests.ps1
index 6d85519..06b9756 100644
--- a/research/experiments/phase_i7/tests/orchestration_tests.ps1
+++ b/research/experiments/phase_i7/tests/orchestration_tests.ps1
@@ -348,3 +348,33 @@ Record 'S10-cal-run' (($r.ExitCode -eq 0) -and ($calFinished.Count -eq 9) -and
+# Verify balanced Latin-square seeds and execution order across arms
+$specA1 = Get-Content -Raw (Join-Path $rootCal 'work\specs\cal_A_01.json') | ConvertFrom-Json
+$specAmin1 = Get-Content -Raw (Join-Path $rootCal 'work\specs\cal_AMIN_01.json') | ConvertFrom-Json
+$specB1 = Get-Content -Raw (Join-Path $rootCal 'work\specs\cal_B_01.json') | ConvertFrom-Json
+$seedsMatchTriplet1 = ($specA1.seed -eq $specAmin1.seed) -and ($specAmin1.seed -eq $specB1.seed)
+$orderCorrect = ($specA1.order_position -eq 1) -and ($specAmin1.order_position -eq 2) -and ($specB1.order_position -eq 3)
+Record 'S10-cal-seeds-and-order' ($seedsMatchTriplet1 -and $orderCorrect) ('triplet 1 shared seed=' + $specA1.seed + ' balanced orders=(1,2,3)')
+
 # Idempotency: Invoking -Calibrate again must execute 0 new processes and return immediately
 $beforeLines = @(Read-Lines $calLedger).Count
 $r = Invoke-Driver -Root $rootCal -ArgList @('-Calibrate', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
 $afterLines = @(Read-Lines $calLedger).Count
 Record 'S10-cal-idempotent' (($r.ExitCode -eq 0) -and ($afterLines -eq $beforeLines) -and ($r.Out -match 'already complete')) 'second -Calibrate is idempotent (0 new runs)'
+
+# Incomplete calibration refusal: if any cell is missing, -Calibrate must fail closed and refuse to produce calibration.json
+$rootCalPartial = New-Root 's10_cal_partial'
+$prepCalPartial = Prepare-Root $rootCalPartial
+$null = New-Approval -Root $rootCalPartial -Gate 'G3' -AllowedBlocks @('PF') -RunlistHash $prepCalPartial.Hash -RunnerHash $stubHash -Scope 'calibration'
+$calDirPartial = Join-Path $rootCalPartial 'calibration'
+New-Item -ItemType Directory -Force -Path $calDirPartial | Out-Null
+$partialLedger = Join-Path $calDirPartial 'calibration_ledger.jsonl'
+# Populate only 8 of 9 cells (omit cal_AMIN_03)
+$allPlanCells = @('cal_A_01','cal_AMIN_01','cal_B_01','cal_AMIN_02','cal_B_02','cal_A_02','cal_B_03','cal_A_03')
+foreach ($cellTid in $allPlanCells) {
+    [System.IO.File]::AppendAllText($partialLedger, ((@{ event = 'STARTED'; trial_id = $cellTid } | ConvertTo-Json -Compress) + "`n"))
+    [System.IO.File]::AppendAllText($partialLedger, ((@{ event = 'FINISHED'; trial_id = $cellTid; status = 'SUCCESS' } | ConvertTo-Json -Compress) + "`n"))
+    $fake = @{ status = 'SUCCESS'; result = @{ throughput = @{ read_ops_per_sec = 1200.0 } } }
+    [System.IO.File]::WriteAllText((Join-Path $calDirPartial ($cellTid + '.json')), ($fake | ConvertTo-Json -Compress))
+}
+# With stub mode set to fail, the missing 9th run will fail
+$env:I7_STUB_MODE = 'fail'
+$r = Invoke-Driver -Root $rootCalPartial -ArgList @('-Calibrate', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
+$calJsonPartial = Join-Path $rootCalPartial 'calibration.json'
+Record 'S10-cal-incomplete-refusal' (($r.ExitCode -ne 0) -and (-not (Test-Path -LiteralPath $calJsonPartial)) -and ($r.Out -match 'calibration incomplete')) 'calibration refuses partial completion (no calibration.json produced)'
+$env:I7_STUB_MODE = 'ok'
```

---

## 3. Deterministic Test Logs

### 3.1 Go Package Unit & Integration Tests
```
$ go test -v -count=1 ./research/experiments/phase_i7/harness2/...
=== RUN   TestKeyStreamIsSharedAcrossArms
--- PASS: TestKeyStreamIsSharedAcrossArms (0.05s)
=== RUN   TestKeyStreamAbsentKeysAreOutsideKeyspace
--- PASS: TestKeyStreamAbsentKeysAreOutsideKeyspace (0.00s)
=== RUN   TestStatsPercentilesAndSufficiency
--- PASS: TestStatsPercentilesAndSufficiency (0.00s)
=== RUN   TestShardMergePreservesSamples
--- PASS: TestShardMergePreservesSamples (0.00s)
=== RUN   TestScheduleDeterministicAndRateAccurate
--- PASS: TestScheduleDeterministicAndRateAccurate (0.00s)
=== RUN   TestConditionEventsBlockRatio
--- PASS: TestConditionEventsBlockRatio (0.00s)
=== RUN   TestResolveModeRejectsUnknownCombinations
--- PASS: TestResolveModeRejectsUnknownCombinations (0.00s)
=== RUN   TestRunClosedLoopAllArms
=== RUN   TestRunClosedLoopAllArms/A
=== RUN   TestRunClosedLoopAllArms/AMIN
=== RUN   TestRunClosedLoopAllArms/B
--- PASS: TestRunClosedLoopAllArms (1.36s)
    --- PASS: TestRunClosedLoopAllArms/A (0.32s)
    --- PASS: TestRunClosedLoopAllArms/AMIN (0.33s)
    --- PASS: TestRunClosedLoopAllArms/B (0.70s)
=== RUN   TestRunPublicationOnlyDriver
--- PASS: TestRunPublicationOnlyDriver (0.56s)
=== RUN   TestRunPacedWriterMatchesTargetRate
--- PASS: TestRunPacedWriterMatchesTargetRate (0.31s)
=== RUN   TestRunOpenLoopAccounting
--- PASS: TestRunOpenLoopAccounting (0.30s)
=== RUN   TestE2_PhysicalVsNoneModeComparison
--- PASS: TestE2_PhysicalVsNoneModeComparison (1.65s)
=== RUN   TestE2_EventRecorderWindowPartitioning
--- PASS: TestE2_EventRecorderWindowPartitioning (0.00s)
=== RUN   TestE4_OpenLoopOverloadAndLateAccounting
--- PASS: TestE4_OpenLoopOverloadAndLateAccounting (0.31s)
=== RUN   TestRunAllMissLengthensReadPath
--- PASS: TestRunAllMissLengthensReadPath (0.31s)
=== RUN   TestRunRefusesDirtyDataDir
--- PASS: TestRunRefusesDirtyDataDir (0.00s)
=== RUN   TestHarnessEnvironmentTypeCompatible
--- PASS: TestHarnessEnvironmentTypeCompatible (0.00s)
PASS
ok      owndbms/research/experiments/phase_i7/harness2  5.652s
```

### 3.2 LSM Engine Unit & Concurrency Tests
```
$ go test -count=1 ./lsmengine/...
ok      owndbms/lsmengine       16.809s
```

### 3.3 Synthetic PowerShell Orchestration Test Suite
```
$ powershell -ExecutionPolicy Bypass -File .\research\experiments\phase_i7\tests\orchestration_tests.ps1
Building orchestration stub runner
stub: C:\Users\ramst\AppData\Local\Temp\i7_orch_20261028130224\i7stub.exe sha256 c8366c7a7bf935f22f964d42c802d544249a3f42503a24dbbbd6f982ba913378

S1/S2: -Check and -DryRun
  [PASS] S1-check - platform manifest written
  [PASS] S2-runlist - runlist rows = 366
  [PASS] S2-anchor-start - first rows: D3/start
  [PASS] S2-anchor-end - last rows: D3/end
  [PASS] S2-hash-recorded - manifest binds the runlist hash

S3: approval template
  [PASS] S3-template - template written with approved=false and the current runlist hash

S4: approval binding
  [PASS] S4-runlist-binding - wrong runlist hash refused
  [PASS] S4-runner-binding - wrong runner hash refused
  [PASS] S4-block-binding - unapproved block refused
  [PASS] S4-dirty-binding - dirty tree refused without allow_dirty
  [PASS] S4-gate-binding - wrong gate refused

S5: clean run + package
  [PASS] S5-run - finished=6 success=6
  [PASS] S5-package - zip + checksums + summary present

S6: crash then resume
  [PASS] S6-crash-recorded - started=6 failed=6
  [PASS] S6-resume - success after resume = 6
  [PASS] S6-replacements - replacements_used = 6
  [PASS] S6-idempotent-resume - ledger lines unchanged: 24

S7: trial budget
  [PASS] S7-max-trials - started = 3 with -MaxTrials 3
  [PASS] S7-ceiling-blocks - second run refused: attempts already at the -MaxTrials ceiling
  [PASS] S7-timeout - status = TIMEOUT

S8: envelope validation
  [PASS] S8-invalid-envelope - status = INVALID

S9: safety and mode gating
  [PASS] S9-frozen-root - results root inside a frozen campaign directory refused
  [PASS] S9-diagnostics-gate - diagnostics requires the G5 gate
  [PASS] S9-gate-mode - preflight rejects a G4 gate
  [PASS] S9-override-guard - runner override without -TestMode refused

S10: calibration correctness
  [PASS] S10-cal-scope-refusal - calibration rejects non-calibration approval scope
  [PASS] S10-cal-template - calibration approval template generated
  [PASS] S10-cal-run - calibration completed: finished=9
  [PASS] S10-cal-seeds-and-order - triplet 1 shared seed=819238815332965397 balanced orders=(1,2,3)
  [PASS] S10-cal-idempotent - second -Calibrate is idempotent (0 new runs)
  [PASS] S10-cal-incomplete-refusal - calibration refuses partial completion (no calibration.json produced)
  [PASS] S10-cal-restart-safe - resumed partial calibration: total success = 9

scenarios: 32  passed: 32  failed: 0
```

---

## 4. Revised G3 Calibration Instructions (Awaiting Authorization)

Execution remains **STRICTLY STOPPED**. Do **not** execute without external authorization.

When authorized, the exact sequence to execute the 9 calibration trials is:

### Step 1: Re-validate platform environment and build runner
```powershell
powershell -ExecutionPolicy Bypass -File .\research\experiments\phase_i7\run_i7.ps1 -Check
```

### Step 2: Generate dedicated calibration approval template
```powershell
powershell -ExecutionPolicy Bypass -File .\research\experiments\phase_i7\run_i7.ps1 -WriteApprovalTemplate -Gate G3 -Scope calibration
```

### Step 3: Authorize calibration approval file
External reviewer signs `APPROVAL.json` with `approved: true`, binding:
- `gate`: `"G3"`
- `scope`: `"calibration"`
- `allowed_blocks`: `["PF"]`
- `runner_sha256`: matching `bin\i7trial.exe`
- `runlist_sha256`: matching `runlist.csv`

### Step 4: Execute calibrated measurement
```powershell
powershell -ExecutionPolicy Bypass -File .\research\experiments\phase_i7\run_i7.ps1 -Calibrate
```

Upon successful completion of all 9 trials, `calibration.json` will be generated with $C_{\text{ref}}$ and per-arm medians.
