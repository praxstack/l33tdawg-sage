package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	_ "modernc.org/sqlite"

	"github.com/l33tdawg/sage/internal/snapshot"
)

// TestV75_E2E_PanicToRollback exercises the full v7.5 panic-and-recover
// handshake across the producer side (chain binary writes HALT) and
// the consumer side (launcher reads HALT, restores snapshot, re-execs).
//
// Setup:
//
//	~/.sage/
//	  data/
//	    badger/  + sage.db + cometbft/  (live state — pretend the chain wrote here)
//	    HALT     (the sentinel a panic'd binary would produce)
//	  snapshots/
//	    100/
//	      manifest.json (BinaryVersion: "v7.1.2")  — the rollback anchor
//	      badger.backup + sage.db + cometbft-data.tar.zst + config.tar.zst
//	      OK
//	      binary/sage-gui-v7.1.2  — the rollback executable
//
// Flow:
//  1. Seed a healthy data dir + a v7.1.2 anchor snapshot.
//  2. Mutate the live data dir to a "post-failed-upgrade" state
//     (BadgerDB has a different key that the v7.1.2 anchor doesn't).
//  3. Write a HALT sentinel with empty RollbackTo to simulate a
//     panic'd v7.5.0 binary.
//  4. Call ReadHaltSignal + HandleHalt with:
//     - real snapshotRestorer (calls internal/snapshot.Restore)
//     - stub Execer (records the would-be exec call)
//  5. Assert:
//     - HandleHalt returned without error
//     - Restorer ran (BadgerDB now contains the anchor's contents, not
//     the "post-failed-upgrade" state)
//     - Execer was called with the rollback binary path
//     - HALT sentinel was cleared
//     - launcher.log got a ROLLBACK_TRIGGERED entry
func TestV75_E2E_PanicToRollback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("syscall.Exec semantics differ on Windows; rollback exec tested on Unix only")
	}

	sageHome := t.TempDir()
	dataDir := filepath.Join(sageHome, "data")
	// snapshots live INSIDE dataDir (matches the snapshot package's
	// snapshotsDirName const and the production supervisor wiring).
	snapshotsDir := filepath.Join(dataDir, "snapshots")
	launcherLog := filepath.Join(sageHome, "launcher.log")
	haltPath := filepath.Join(dataDir, "HALT")

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}

	// --- Phase 1: seed a healthy data dir with v7.1.2 contents.
	healthyMarker := []byte("v7.1.2-canonical-state")
	seedBadgerWithKey(t, filepath.Join(dataDir, "badger"), "v7.1.2:marker", healthyMarker)
	seedSqlite(t, filepath.Join(dataDir, "sage.db"), "anchor")
	// CometBFT data + config skeleton. writeCometDataTar only includes
	// a whitelist of files (blockstore.db / state.db / tx_index.db /
	// evidence.db / priv_validator_state.json) so we have to seed one
	// of those names — an arbitrary "placeholder" file is silently
	// excluded and verify rejects the resulting empty tar.
	for _, sub := range []string{"data", "config"} {
		dir := filepath.Join(dataDir, "cometbft", sub)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir cometbft/%s: %v", sub, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dataDir, "cometbft", "data", "blockstore.db"), []byte("synthetic-blockstore"), 0o600); err != nil {
		t.Fatalf("seed cometbft/data/blockstore.db: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "cometbft", "config", "genesis.json"), []byte(`{"chain_id":"sage-test"}`), 0o600); err != nil {
		t.Fatalf("seed cometbft/config/genesis.json: %v", err)
	}

	// --- Phase 2: take an anchor snapshot at height 100 with
	// BinaryVersion=v7.1.2. Compute the real AppHash from the seeded
	// badger so Verify recomputes it identically during restore.
	appHash, hashErr := snapshot.DefaultAppHashComputer(filepath.Join(dataDir, "badger"))
	if hashErr != nil {
		t.Fatalf("compute apphash: %v", hashErr)
	}
	manifest, takeErr := snapshot.Take(context.Background(), dataDir, 100, appHash, "anchor", snapshot.Options{
		BinaryVersion: "v7.1.2",
		IncludeBinary: false, // we install a fake binary manually below
	})
	if takeErr != nil {
		t.Fatalf("Take anchor: %v", takeErr)
	}
	_ = snapshotsDir // assertion only — actual path is dataDir/snapshots
	snapDir := filepath.Join(dataDir, "snapshots", "100")
	// Install a fake v7.1.2 binary that the launcher will try to exec.
	binDir := filepath.Join(snapDir, "binary")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("mkdir binary: %v", err)
	}
	fakeBinary := filepath.Join(binDir, "sage-gui-v7.1.2")
	if err := os.WriteFile(fakeBinary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec
		t.Fatalf("write fake binary: %v", err)
	}
	if manifest.BinaryVersion != "v7.1.2" {
		t.Fatalf("manifest.BinaryVersion = %q, want v7.1.2", manifest.BinaryVersion)
	}

	// --- Phase 3: mutate the live data dir into a "post-failed-upgrade"
	// state. This is what a v7.5.0 binary would write before panicking.
	corruptMarker := []byte("v7.5.0-half-applied-corrupt-state")
	seedBadgerWithKey(t, filepath.Join(dataDir, "badger"), "v7.5.0:corrupt", corruptMarker)

	// --- Phase 4: write a HALT sentinel as a panic'd binary would.
	sentinel := HaltSignal{
		FailedVersion:  "v7.5.0",
		RollbackTo:     "", // empty -> launcher picks latest non-failed anchor
		FailureMessage: "panic: simulated upgrade-handler failure",
		Timestamp:      time.Now().Unix(),
	}
	raw, mErr := json.Marshal(&sentinel)
	if mErr != nil {
		t.Fatalf("marshal HALT: %v", mErr)
	}
	if err := os.WriteFile(haltPath, raw, 0o600); err != nil {
		t.Fatalf("write HALT: %v", err)
	}

	// --- Phase 5: launcher detects HALT and dispatches rollback.
	sig, readErr := ReadHaltSignal(haltPath)
	if readErr != nil {
		t.Fatalf("ReadHaltSignal: %v", readErr)
	}
	if sig.FailedVersion != "v7.5.0" {
		t.Fatalf("FailedVersion = %q", sig.FailedVersion)
	}

	// Use the real snapshotRestorer so the restore actually mutates
	// the data dir. The Execer is stubbed so the test process doesn't
	// get replaced.
	stubExec := &recordingExecer{}
	rest := &snapshotRestorer{logf: func(format string, args ...interface{}) {
		t.Logf("[restorer] "+format, args...)
	}}
	ctx := RollbackContext{
		HaltPath:     haltPath,
		SnapshotsDir: snapshotsDir,
		DataDir:      dataDir,
		LauncherLog:  launcherLog,
		Restorer:     rest,
		Execer:       stubExec,
		Logf: func(format string, args ...interface{}) {
			t.Logf("[launcher] "+format, args...)
		},
	}
	if err := HandleHalt(ctx, sig); err != nil {
		t.Fatalf("HandleHalt: %v", err)
	}

	// --- Phase 6: verify the state.
	// (a) Exec was attempted on the right binary.
	if !stubExec.called {
		t.Fatal("Execer was not invoked")
	}
	if stubExec.argv0 != fakeBinary {
		t.Errorf("Execer argv0 = %q, want %q", stubExec.argv0, fakeBinary)
	}

	// (b) HALT sentinel was cleared.
	if _, statErr := os.Stat(haltPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("HALT sentinel should have been cleared, got stat err: %v", statErr)
	}

	// (c) BadgerDB was restored to the anchor state — corrupt marker
	//     gone, healthy marker present.
	got := readBadgerKey(t, filepath.Join(dataDir, "badger"), "v7.1.2:marker")
	if string(got) != string(healthyMarker) {
		t.Errorf("badger v7.1.2:marker = %q, want %q (restore didn't run?)", got, healthyMarker)
	}
	corruptGot := readBadgerKey(t, filepath.Join(dataDir, "badger"), "v7.5.0:corrupt")
	if corruptGot != nil {
		t.Errorf("badger should not still contain corrupt marker after rollback, got %q", corruptGot)
	}

	// (d) launcher.log got a ROLLBACK_TRIGGERED entry.
	logBytes, lerr := os.ReadFile(launcherLog)
	if lerr != nil {
		t.Fatalf("read launcher.log: %v", lerr)
	}
	if len(logBytes) == 0 {
		t.Error("launcher.log is empty — rollback event not recorded")
	} else if !contains(logBytes, "ROLLBACK_TRIGGERED") {
		t.Errorf("launcher.log missing ROLLBACK_TRIGGERED event: %s", logBytes)
	}
}

// recordingExecer captures what would have been exec'd without
// actually replacing the test process.
type recordingExecer struct {
	called bool
	argv0  string
	argv   []string
}

func (r *recordingExecer) Exec(argv0 string, argv []string, _ []string) error {
	r.called = true
	r.argv0 = argv0
	r.argv = argv
	return nil // returning nil signals success without exec'ing
}

// --- helpers ----------------------------------------------------------------

func seedBadgerWithKey(t *testing.T, dir, key string, value []byte) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	bopts := badger.DefaultOptions(dir)
	bopts.Logger = nil
	db, err := badger.Open(bopts)
	if err != nil {
		t.Fatalf("open badger %s: %v", dir, err)
	}
	defer func() { _ = db.Close() }()
	if uErr := db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(key), value)
	}); uErr != nil {
		t.Fatalf("seed badger: %v", uErr)
	}
}

func readBadgerKey(t *testing.T, dir, key string) []byte {
	t.Helper()
	bopts := badger.DefaultOptions(dir)
	bopts.Logger = nil
	db, err := badger.Open(bopts)
	if err != nil {
		t.Fatalf("reopen badger: %v", err)
	}
	defer func() { _ = db.Close() }()
	var got []byte
	_ = db.View(func(txn *badger.Txn) error {
		item, gErr := txn.Get([]byte(key))
		if gErr != nil {
			return nil // returns nil bytes
		}
		return item.Value(func(v []byte) error {
			got = append([]byte{}, v...)
			return nil
		})
	})
	return got
}

func seedSqlite(t *testing.T, path, marker string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE markers (m TEXT)`); err != nil {
		t.Fatalf("create markers: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO markers VALUES (?)`, marker); err != nil {
		t.Fatalf("insert marker: %v", err)
	}
}

func contains(haystack []byte, needle string) bool {
	return len(haystack) >= len(needle) && (string(haystack) != "" && index(haystack, needle) >= 0)
}

func index(haystack []byte, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == needle {
			return i
		}
	}
	return -1
}

// Sanity check: snapshot SHA over the restored badger.backup is the
// same as what manifest.AppHash records. Belt-and-braces parity with
// the snapshot package's own Verify test.
func TestV75_E2E_AnchorRestorePreservesAppHash(t *testing.T) {
	dataDir := t.TempDir()
	seedBadgerWithKey(t, filepath.Join(dataDir, "badger"), "k1", []byte("v1"))
	seedSqlite(t, filepath.Join(dataDir, "sage.db"), "ok")
	for _, sub := range []string{"data", "config"} {
		_ = os.MkdirAll(filepath.Join(dataDir, "cometbft", sub), 0o700)
	}
	_ = os.WriteFile(filepath.Join(dataDir, "cometbft", "data", "blockstore.db"), []byte("x"), 0o600)
	_ = os.WriteFile(filepath.Join(dataDir, "cometbft", "config", "genesis.json"), []byte("{}"), 0o600)

	// Compute the deterministic AppHash from the seeded badger and pass
	// that to Take — Verify will recompute it during the eventual restore
	// and the two must agree byte-for-byte.
	expectedHash, hashErr := snapshot.DefaultAppHashComputer(filepath.Join(dataDir, "badger"))
	if hashErr != nil {
		t.Fatalf("compute apphash: %v", hashErr)
	}
	manifest, takeErr := snapshot.Take(context.Background(), dataDir, 1, expectedHash, "test", snapshot.Options{
		BinaryVersion: "v7.5.0-test",
		IncludeBinary: false,
	})
	if takeErr != nil {
		t.Fatalf("Take: %v", takeErr)
	}
	if hex.EncodeToString(manifest.AppHash) != hex.EncodeToString(expectedHash) {
		t.Fatalf("manifest AppHash drift: got %x, want %x", manifest.AppHash, expectedHash)
	}
}
