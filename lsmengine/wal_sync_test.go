package lsmengine

import "testing"

func TestSetWALSync(t *testing.T) {
	for _, initial := range []bool{false, true} {
		m := newReadPathManager(t, Config{DisableWALSync: initial})
		for _, enabled := range []bool{true, false, true} {
			m.SetWALSync(enabled)
			if m.Log.DisableSync.Load() == enabled {
				t.Fatal("current WAL lost setting")
			}
			tx := m.Begin()
			tx.Put("toggle", "value")
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			flushReadPathManager(t, m)
			if m.Log.DisableSync.Load() == enabled {
				t.Fatal("rotated WAL lost setting")
			}
			m.SetMemtableThresholdBytes(1)
			tx = m.Begin()
			tx.Put("threshold", "value")
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := m.WaitForFlushCompletion(); err != nil {
				t.Fatal(err)
			}
			if m.Log.DisableSync.Load() == enabled {
				t.Fatal("automatic rotation lost setting")
			}
		}
	}
}
