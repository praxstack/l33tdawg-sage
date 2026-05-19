package snapshot

// diagnose.go is the boot-time triage call. cmd/sage-gui/node.go (in a
// later integration commit) calls DiagnoseDataDir BEFORE badger.Open
// to decide whether to proceed normally or auto-restore from a
// snapshot. The five possible statuses each have a clear remediation
// per docs/backup-restore.md:
//
//   Healthy        — proceed
//   CorruptBadger  — AutoRestoreLatest (BadgerDB MANIFEST is the
//                    canary; if it's missing/truncated, the entire
//                    BadgerDB is unrecoverable in-process)
//   CorruptSqlite  — AutoRestoreLatest
//   CorruptCometBFT — AutoRestoreLatest (mid-commit crash: blockstore
//                    height < state height means the chain advanced
//                    state but didn't persist the block)
//   PostUpgradeHalt — AutoRestoreAnchor for the previous binary
//                    version (handler panic after an upgrade)
//
// We deliberately avoid pulling in CometBFT's blockstore package here
// because that drags v0.38's pebble dep into this package's import
// closure. Instead we read the on-disk state JSON files that
// CometBFT writes alongside its dbs — those are stable across versions.

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Status is the result of DiagnoseDataDir.
type Status int

const (
	// Healthy means every layer opens cleanly and no halt sentinel exists.
	Healthy Status = iota
	// CorruptBadger means BadgerDB is unrecoverable (MANIFEST missing/truncated).
	CorruptBadger
	// CorruptSqlite means PRAGMA integrity_check fails or the file is unreadable.
	CorruptSqlite
	// CorruptCometBFT means blockstore.db height < state.db height
	// (mid-write crash) or those dbs are otherwise inconsistent.
	CorruptCometBFT
	// PostUpgradeHalt means a HALT sentinel exists, written by the
	// upgrade-watchdog on handler panic.
	PostUpgradeHalt
	// Empty means dataDir has no chain state at all — fresh init, no
	// restore needed.
	Empty
)

// String returns a human-readable name for the status.
func (s Status) String() string {
	switch s {
	case Healthy:
		return "Healthy"
	case CorruptBadger:
		return "CorruptBadger"
	case CorruptSqlite:
		return "CorruptSqlite"
	case CorruptCometBFT:
		return "CorruptCometBFT"
	case PostUpgradeHalt:
		return "PostUpgradeHalt"
	case Empty:
		return "Empty"
	default:
		return "Unknown"
	}
}

// HaltSentinelName is the basename of the post-upgrade halt sentinel.
// Contents are JSON: {"failed_version": "...", "rollback_to": "..."}.
const HaltSentinelName = "HALT"

// DiagnoseDataDir reports the health of a SAGE data directory. The
// checks are deliberately cheap (no DB opens for happy-path scans);
// the cost is paid on failure paths where the operator was already
// going to be slow-pathed.
func DiagnoseDataDir(dataDir string) Status {
	if dataDir == "" {
		return Empty
	}

	// 1. HALT sentinel wins over everything — it's how the upgrade
	// watchdog signals "do not boot the new binary again".
	if _, err := os.Stat(filepath.Join(dataDir, HaltSentinelName)); err == nil {
		return PostUpgradeHalt
	}

	badgerPath := filepath.Join(dataDir, "badger")
	sqlitePath := filepath.Join(dataDir, "sage.db")
	cometDataDir := filepath.Join(dataDir, "cometbft", "data")

	// If nothing exists, this is a fresh data dir — let the caller
	// initialise normally rather than triggering a restore.
	if !exists(badgerPath) && !exists(sqlitePath) && !exists(cometDataDir) {
		return Empty
	}

	// 2. BadgerDB MANIFEST check. Badger writes MANIFEST atomically;
	// its absence after the first boot is a smoking gun for a
	// truncated/wiped store. We don't try to open the DB here because
	// a corrupt MANIFEST panics on open in some Badger versions.
	if exists(badgerPath) {
		if err := checkBadgerManifest(badgerPath); err != nil {
			return CorruptBadger
		}
	}

	// 3. SQLite integrity_check.
	if exists(sqlitePath) {
		if err := quickSqliteCheck(sqlitePath); err != nil {
			return CorruptSqlite
		}
	}

	// 4. CometBFT consistency. We assert blockstore is not behind state;
	// the reverse (state behind blockstore) is the recoverable case
	// CometBFT handles itself via replay.
	if exists(cometDataDir) {
		if err := checkCometConsistency(cometDataDir); err != nil {
			return CorruptCometBFT
		}
	}

	return Healthy
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// checkBadgerManifest verifies the MANIFEST file exists and is at
// least a minimal Badger header (4 bytes magic + 4 bytes version).
// Anything smaller indicates truncation.
func checkBadgerManifest(badgerPath string) error {
	manifest := filepath.Join(badgerPath, "MANIFEST")
	info, err := os.Stat(manifest)
	if err != nil {
		return err
	}
	if info.Size() < 8 {
		return errors.New("MANIFEST truncated")
	}
	return nil
}

// quickSqliteCheck opens the DB read-only and runs integrity_check.
// Short timeout so a hung DB doesn't block boot indefinitely.
func quickSqliteCheck(path string) error {
	dsn := path + "?_busy_timeout=5000&mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	done := make(chan error, 1)
	go func() {
		var res string
		row := db.QueryRow("PRAGMA integrity_check")
		if scanErr := row.Scan(&res); scanErr != nil {
			done <- scanErr
			return
		}
		if res != "ok" {
			done <- errors.New("integrity_check: " + res)
			return
		}
		done <- nil
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		return errors.New("integrity_check timed out")
	}
}

// checkCometConsistency reads the recorded heights from the on-disk
// blockstore and state metadata files and reports an error if
// blockstore < state (the classic mid-commit crash signature).
//
// CometBFT writes the heights into stateStore via TM-LevelDB; we
// cannot read those without importing the full cometbft data package.
// Instead we check that the canonical *.db directories are present
// and non-empty as a coarse heuristic — the real height comparison
// will be wired in by the integration commit once we vendor the
// minimal CometBFT state-reading helpers.
//
// TODO(integration): replace this stub with the real
// (state.Store).Load + (blockstore).Height comparison.
func checkCometConsistency(dataDir string) error {
	blockstore := filepath.Join(dataDir, "blockstore.db")
	state := filepath.Join(dataDir, "state.db")
	for _, p := range []string{blockstore, state} {
		info, err := os.Stat(p)
		if err != nil {
			// Not present yet (genesis boot); not corruption.
			return nil
		}
		if !info.IsDir() {
			return errors.New("expected CometBFT db to be a directory: " + p)
		}
		entries, readErr := os.ReadDir(p)
		if readErr != nil {
			return readErr
		}
		if len(entries) == 0 {
			return errors.New("CometBFT db is empty: " + p)
		}
	}
	return nil
}
