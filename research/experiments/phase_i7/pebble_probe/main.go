// Command pebble_probe is a small, isolated Windows-native smoke test for
// github.com/cockroachdb/pebble. It exercises the open/put/get/flush/close,
// reopen/get/close lifecycle in a fresh temp directory (outside the repo),
// then checks that the directory can be fully removed (a Windows
// file-handle-leak check).
//
// It prints exactly one JSON object to stdout and exits 0 only when every
// *_ok field is true.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	numKeys  = 200
	valueLen = 128
)

// result holds the single JSON object written to stdout.
type result struct {
	PebbleVersion   string   `json:"pebble_version"`
	GoVersion       string   `json:"go_version"`
	GOOS            string   `json:"goos"`
	GOARCH          string   `json:"goarch"`
	OpenOK          bool     `json:"open_ok"`
	SetOK           bool     `json:"set_ok"`
	GetOK           bool     `json:"get_ok"`
	FlushOK         bool     `json:"flush_ok"`
	CloseOK         bool     `json:"close_ok"`
	ReopenOK        bool     `json:"reopen_ok"`
	RegetOK         bool     `json:"reget_ok"`
	FilesAfterClose []string `json:"files_after_close"`
	RemoveAllOK     bool     `json:"removeall_ok"`
	Error           string   `json:"error"`
	ElapsedMS       int64    `json:"elapsed_ms"`
}

func userKey(i int) []byte {
	return []byte(fmt.Sprintf("user_key_%010d", i))
}

func userValue(i int) []byte {
	// Deterministic 128-byte value that is unique per key.
	v := make([]byte, valueLen)
	prefix := fmt.Sprintf("value_for_%010d|", i)
	copy(v, prefix)
	for j := len(prefix); j < valueLen; j++ {
		v[j] = byte('a' + (i+j)%26)
	}
	return v
}

// pebbleVersionFromBuildInfo reports the resolved module version of pebble.
func pebbleVersionFromBuildInfo() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dep := range bi.Deps {
		if dep.Path == "github.com/cockroachdb/pebble" {
			return dep.Version
		}
	}
	return "unknown"
}

func run() (res result) {
	start := time.Now()
	res = result{
		PebbleVersion:   pebbleVersionFromBuildInfo(),
		GoVersion:       runtime.Version(),
		GOOS:            runtime.GOOS,
		GOARCH:          runtime.GOARCH,
		FilesAfterClose: []string{},
	}
	// fail records the first error and stops the probe at that point.
	fail := func(stage string, err error) {
		if res.Error == "" {
			res.Error = fmt.Sprintf("%s: %v", stage, err)
		}
		res.ElapsedMS = time.Since(start).Milliseconds()
	}

	dir, err := os.MkdirTemp("", "pebble_probe_*")
	if err != nil {
		fail("mkdirtemp", err)
		return res
	}
	// Belt-and-braces cleanup; the explicit RemoveAll below is the real check.
	defer os.RemoveAll(dir)

	dbPath := filepath.Join(dir, "db")
	db, err := pebble.Open(dbPath, &pebble.Options{})
	if err != nil {
		fail("open", err)
		return res
	}
	res.OpenOK = true

	// --- Set 200 keys with Sync:true -------------------------------------
	wo := &pebble.WriteOptions{Sync: true}
	for i := 0; i < numKeys; i++ {
		if err := db.Set(userKey(i), userValue(i), wo); err != nil {
			fail(fmt.Sprintf("set(key=%s)", userKey(i)), err)
			return res
		}
	}
	res.SetOK = true

	// --- Get 5 keys and verify exact value equality ----------------------
	sample := []int{0, 1, 42, 137, 199}
	for _, i := range sample {
		got, closer, err := db.Get(userKey(i))
		if err != nil {
			fail(fmt.Sprintf("get(key=%s)", userKey(i)), err)
			return res
		}
		want := userValue(i)
		if len(got) != len(want) || string(got) != string(want) {
			closer.Close()
			fail(fmt.Sprintf("get(key=%s)", userKey(i)),
				fmt.Errorf("value mismatch: got %d bytes, want %d bytes", len(got), len(want)))
			return res
		}
		if err := closer.Close(); err != nil {
			fail(fmt.Sprintf("get-closer(key=%s)", userKey(i)), err)
			return res
		}
	}
	res.GetOK = true

	if err := db.Flush(); err != nil {
		fail("flush", err)
		return res
	}
	res.FlushOK = true

	if err := db.Close(); err != nil {
		fail("close", err)
		return res
	}
	res.CloseOK = true

	// --- Reopen the same directory, re-get, close ------------------------
	// The directory is deliberately NOT removed before this reopen.
	db2, err := pebble.Open(dbPath, &pebble.Options{})
	if err != nil {
		fail("reopen", err)
		return res
	}
	res.ReopenOK = true

	for _, i := range sample {
		got, closer, err := db2.Get(userKey(i))
		if err != nil {
			fail(fmt.Sprintf("reget(key=%s)", userKey(i)), err)
			return res
		}
		want := userValue(i)
		bad := len(got) != len(want) || string(got) != string(want)
		if err := closer.Close(); err != nil {
			fail(fmt.Sprintf("reget-closer(key=%s)", userKey(i)), err)
			return res
		}
		if bad {
			fail(fmt.Sprintf("reget(key=%s)", userKey(i)),
				fmt.Errorf("value mismatch after reopen: got %d bytes, want %d bytes", len(got), len(want)))
			return res
		}
	}
	res.RegetOK = true

	if err := db2.Close(); err != nil {
		fail("reclose", err)
		return res
	}

	// --- File listing after close (before removal) -----------------------
	// List the Pebble DB directory itself, i.e. the files Pebble created.
	entries, err := os.ReadDir(dbPath)
	if err != nil {
		fail("readdir", err)
		return res
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		files = append(files, name)
	}
	res.FilesAfterClose = files

	// --- RemoveAll: the Windows file-handle check ------------------------
	if err := os.RemoveAll(dir); err != nil {
		fail("removeall", err)
		return res
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		fail("removeall-verify", fmt.Errorf("directory still present after RemoveAll: %s", dir))
		return res
	}
	res.RemoveAllOK = true

	res.ElapsedMS = time.Since(start).Milliseconds()
	return res
}

func main() {
	res := run()
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(res); err != nil {
		fmt.Fprintf(os.Stderr, "encoding result: %v\n", err)
		os.Exit(1)
	}
	ok := res.OpenOK && res.SetOK && res.GetOK && res.FlushOK &&
		res.CloseOK && res.ReopenOK && res.RegetOK && res.RemoveAllOK
	if !ok {
		os.Exit(1)
	}
}
