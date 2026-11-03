package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

)

func TestSettledPlan(t *testing.T) {
	const master = 20261107
	p := makeSettledPlan(master)
	if p.CampaignID != "X-settled-v1" || p.SettleMs != 20000 || len(p.Cells) != 17 || len(p.Trials) != 470 {
		t.Fatal("dimensions/pause")
	}
	if !reflect.DeepEqual(p, makeSettledPlan(master)) || reflect.DeepEqual(p, makeSettledPlan(master+1)) {
		t.Fatal("determinism/seed sensitivity")
	}
	ids := []string{"S1_d0000_f20_closed", "S1_d0000_f20_open", "S1_d0200_f20_closed", "S1_d0200_f20_open",
		"S1_d2000_f20_closed", "S1_d2000_f20_open", "S2_d0200_w700_closed", "S2_d0200_w700_open", "S3_d0000_f80_closed",
		"R_X1_r16_d0020", "R_X1_r16_d2000", "R_X2_r16_d0200", "R_X3_r16", "R_C1_r16_d0200_on", "R_T1_n256_r16_w0",
		"R_I1_r16_d0200_second", "R_I1_r16_d0200_inline"}
	originals := []string{"P1_d0000_f20_closed", "P1_d0000_f20_open", "P1_d0200_f20_closed", "P1_d0200_f20_open",
		"P1_d2000_f20_closed", "P1_d2000_f20_open", "P1_d0200_f20_closed", "P1_d0200_f20_open", "P1_d0000_f80_closed",
		"X1_r16_d0020", "X1_r16_d2000", "X2_r16_d0200", "X3_r16", "C1_r16_d0200_on", "T1_n256_r16_w0",
		"I1_r16_d0200", "I1_r16_d0200"}
	sources := [][]Cell{makePacedPlan(0).Cells, cells(), makeCompactionPlan(0).Cells, makeTablecountPlan(0).Cells, makeInterventionPlan(0).Cells}
	for i, c := range p.Cells {
		si := 0
		switch {
		case i >= 15:
			si = 4
		case i == 14:
			si = 3
		case i == 13:
			si = 2
		case i >= 9:
			si = 1
		}
		want := sourceCell(sources[si], originals[i])
		want.Cell, want.DiagTrace = ids[i], i < 9
		if i == 6 || i == 7 {
			want.PacedWriteRatePerSec = 700
		}
		if i >= 15 {
			want.InlineFlushCheck = i == 16
		}
		if c.ID != ids[i] || !reflect.DeepEqual(c.Spec, want) {
			t.Fatalf("source spec mismatch at %d: %+v", i, c)
		}
		if _, err := c.Spec.ResolveMode(); err != nil {
			t.Fatal(c.ID, err)
		}
	}
	seen, counts := map[string]bool{}, map[string]map[string]int{}
	row := 0
	for k := 1; k <= 10; k++ {
		n := 15
		if k <= 5 {
			n = 16
		}
		for _, i := range rand.New(rand.NewSource(master + int64(k))).Perm(n) {
			entry, order := ids[i], []int{0, 1, 2}
			if i == 15 {
				entry = "R_I1_r16_d0200"
				order = rand.New(rand.NewSource(master*1000 + int64(k)*100 + int64(i))).Perm(4)
			}
			for pos, run := range order {
				want, arm := p.Cells[i].Spec, permutations[(k+i)%6][pos%3]
				if i == 15 {
					want, arm = p.Cells[15+run/2].Spec, []string{"A", "AMIN"}[run%2]
				}
				want.Triplet, want.Seed, want.Arm = k, pairedSeed(master, entry, k), arm
				want.OrderPosition, want.RunlistPosition = pos+1, row+1
				want.TrialID = fmt.Sprintf("%s_t%02d_%s", want.Cell, k, arm)
				got := p.Trials[row]
				if seen[got.TrialID] || !reflect.DeepEqual(got, want) {
					t.Fatalf("schedule row %d: %+v", row, got)
				}
				if _, err := got.ResolveMode(); err != nil {
					t.Fatal(err)
				}
				seen[got.TrialID] = true
				if counts[got.Cell] == nil {
					counts[got.Cell] = map[string]int{}
				}
				counts[got.Cell][arm]++
				row++
			}
		}
	}
	for i, id := range ids {
		want := map[string]int{"A": 10, "AMIN": 10, "B": 10}
		if i >= 15 {
			want = map[string]int{"A": 5, "AMIN": 5}
		}
		if !reflect.DeepEqual(counts[id], want) {
			t.Fatal(id, counts[id])
		}
	}
	b, err := jsonBytes(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("settled plan SHA-256:", digest(b))
	for _, old := range []Plan{makePlan(20261030), makeInterventionPlan(20261103), makeCompactionPlan(20261104), makeTablecountPlan(20261105), makePacedPlan(20261106)} {
		b, err := jsonBytes(old)
		if err != nil || bytes.Contains(b, []byte("settle_ms")) {
			t.Fatal("older plan gained settle_ms", err)
		}
	}
}

func TestSettledPauseBeforeEveryAttempt(t *testing.T) {
	t.Setenv("XCAMPAIGN_TEST_HELPER", "retry")
	runner, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := makeSettledPlan(20261107)
	p.Trials, p.SettleMs = p.Trials[:2], 25
	o := options{runner: runner, out: dir, data: filepath.Join(dir, "data"), timeout: 5 * time.Second}
	before := time.Now()
	if err := execute(context.Background(), o, p, platform{}, io.Discard); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(dir, "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner, starts, previous := bufio.NewScanner(f), 0, before
	for scanner.Scan() {
		var r record
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if r.Event == "STARTED" {
			start, err := time.Parse(time.RFC3339Nano, r.StartUTC)
			if err != nil {
				t.Fatal(err)
			}
			if start.Sub(previous) < 25*time.Millisecond {
				t.Fatal("pause not honored", r)
			}
			starts++
		} else {
			previous, err = time.Parse(time.RFC3339Nano, r.EndUTC)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if starts != 4 {
		t.Fatal("expected first attempts and retries", starts)
	}
}

func TestSettledPauseCancellation(t *testing.T) {
	if err := waitSettle(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	p := makeSettledPlan(20261107)
	p.Trials = p.Trials[:1]
	dir := t.TempDir()
	start := time.Now()
	err := execute(ctx, options{out: dir, data: filepath.Join(dir, "data")}, p, platform{}, io.Discard)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatal("cancellation not prompt", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "ledger.jsonl"))
	if err != nil || len(b) != 0 {
		t.Fatal("STARTED written during cancelled pause", err)
	}
}
