package snapshot

// restore.go reverses Take. The flow is:
//
//  1. Verify(dir) — refuse to restore an unverified snapshot. This is
//     the single most important invariant: divergence is worse than
//     downtime, so we never restore corrupt state.
//  2. Load badger.backup into a fresh BadgerDB at dataDir/badger/.
//  3. Copy sage.db over dataDir/sage.db.
//  4. Untar cometbft-data.tar.zst into dataDir/cometbft/data/.
//  5. Untar config.tar.zst (genesis, validator keys, vault.key).
//  6. Stamp a fresh priv_validator_state.json at height 0 to avoid
//     CometBFT replay-from-future panics.
//
// The caller MUST have closed all live handles before calling Restore.
// In normal operation that's enforced by Restore being called from
// cmd/sage-gui/node.go BEFORE badger.Open / sql.Open. There is no
// in-process "live restore" path — that would require coordinating
// with CometBFT's consensus loop which is out of scope here.

import (
	"archive/tar"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/klauspost/compress/zstd"
)

// RestoreOptions controls Restore behaviour.
type RestoreOptions struct {
	// VaultPassphrase is required when the snapshot manifest's
	// Encrypted flag is true.
	VaultPassphrase string

	// SkipVerify bypasses the Verify(dir) gate. Set ONLY for tests
	// that construct a manifest with no AppHash. Production callers
	// should leave this false.
	SkipVerify bool

	// VaultDestPath, if non-empty, is where the embedded vault.key is
	// written. Defaults to dataDir/../vault.key to match the existing
	// SageHome layout.
	VaultDestPath string
}

// Restore reinstates a snapshot's contents into dataDir. Returns the
// snapshot's height on success.
//
// Caller responsibilities:
//   - No open badger.DB handle on dataDir/badger.
//   - No open sql.DB handle on dataDir/sage.db.
//   - CometBFT not running (its dbs are about to be replaced).
//
// Failure to satisfy these will manifest as "lockfile held" or
// "database is locked" errors from the underlying drivers.
func Restore(dir, dataDir string) (int64, error) {
	return RestoreWithOptions(dir, dataDir, RestoreOptions{})
}

// RestoreWithOptions is the configurable form of Restore.
func RestoreWithOptions(dir, dataDir string, opts RestoreOptions) (int64, error) {
	if dir == "" || dataDir == "" {
		return 0, errors.New("restore: dir and dataDir must be non-empty")
	}

	manifestBytes, err := os.ReadFile(filepath.Join(dir, chunkManifest))
	if err != nil {
		return 0, fmt.Errorf("restore: read manifest: %w", err)
	}
	var m Manifest
	if jerr := json.Unmarshal(manifestBytes, &m); jerr != nil {
		return 0, fmt.Errorf("restore: parse manifest: %w", jerr)
	}

	if !opts.SkipVerify {
		if verr := VerifyWithOptions(dir, VerifyOptions{VaultPassphrase: opts.VaultPassphrase}); verr != nil {
			return 0, fmt.Errorf("restore: pre-verify: %w", verr)
		}
	}

	if mkErr := os.MkdirAll(dataDir, 0o700); mkErr != nil {
		return 0, fmt.Errorf("restore: ensure dataDir: %w", mkErr)
	}

	// 1. BadgerDB — load into a fresh dir, then atomic-swap.
	badgerDst := filepath.Join(dataDir, "badger")
	badgerStaging := filepath.Join(dataDir, "badger.restore-staging")
	_ = os.RemoveAll(badgerStaging)
	if mkErr := os.MkdirAll(badgerStaging, 0o700); mkErr != nil {
		return 0, fmt.Errorf("restore: badger staging: %w", mkErr)
	}

	badgerSrc, badgerCleanup, materErr := materialize(dir, &m, chunkBadger, opts.VaultPassphrase)
	if materErr != nil {
		return 0, fmt.Errorf("restore: materialize badger: %w", materErr)
	}
	defer badgerCleanup()
	if badgerSrc == "" {
		return 0, errors.New("restore: badger backup missing from snapshot")
	}
	if loadErr := loadBadgerBackup(badgerStaging, badgerSrc); loadErr != nil {
		_ = os.RemoveAll(badgerStaging)
		return 0, fmt.Errorf("restore: load badger: %w", loadErr)
	}
	if swapErr := atomicSwapDir(badgerStaging, badgerDst); swapErr != nil {
		_ = os.RemoveAll(badgerStaging)
		return 0, fmt.Errorf("restore: swap badger: %w", swapErr)
	}

	// 2. SQLite — copy decrypted file over the live path.
	sqliteSrc, sqliteCleanup, err := materialize(dir, &m, chunkSQLite, opts.VaultPassphrase)
	if err != nil {
		return 0, fmt.Errorf("restore: materialize sqlite: %w", err)
	}
	defer sqliteCleanup()
	if sqliteSrc != "" {
		sqliteDst := filepath.Join(dataDir, "sage.db")
		// Remove any stale WAL/SHM so the restored file isn't shadowed.
		for _, suffix := range []string{"-wal", "-shm"} {
			_ = os.Remove(sqliteDst + suffix)
		}
		if cpErr := copyFile(sqliteSrc, sqliteDst); cpErr != nil {
			return 0, fmt.Errorf("restore: copy sqlite: %w", cpErr)
		}
		// Sanity-check that the copy opens. Catches partial reads on
		// network filesystems before downstream Open panics.
		if sErr := sanityOpenSQLite(sqliteDst); sErr != nil {
			return 0, fmt.Errorf("restore: sqlite sanity: %w", sErr)
		}
	}

	// 3. CometBFT data — replace the data dir contents.
	cometSrc, cometCleanup, err := materialize(dir, &m, chunkCometData, opts.VaultPassphrase)
	if err != nil {
		return 0, fmt.Errorf("restore: materialize cometbft tar: %w", err)
	}
	defer cometCleanup()
	if cometSrc != "" {
		cometDst := filepath.Join(dataDir, "cometbft", "data")
		// Wipe the existing data dbs but keep priv_validator_state.json
		// — it'll be overwritten by the tar entry if present.
		for _, name := range []string{"blockstore.db", "state.db", "tx_index.db", "evidence.db", "cs.wal"} {
			_ = os.RemoveAll(filepath.Join(cometDst, name))
		}
		if mkErr := os.MkdirAll(cometDst, 0o700); mkErr != nil {
			return 0, fmt.Errorf("restore: cometbft data dir: %w", mkErr)
		}
		if untarErr := untarZstd(cometSrc, cometDst); untarErr != nil {
			return 0, fmt.Errorf("restore: untar cometbft: %w", untarErr)
		}
		// If the tar didn't include priv_validator_state.json, write a
		// fresh height-0 file so the validator boots cleanly.
		pvPath := filepath.Join(cometDst, "priv_validator_state.json")
		if _, statErr := os.Stat(pvPath); statErr != nil {
			_ = os.WriteFile(pvPath, []byte(`{"height":"0","round":0,"step":0}`), 0o600)
		}
	}

	// 4. Config tarball — genesis, node_key, priv_validator_key, vault.key.
	cfgSrc, cfgCleanup, err := materialize(dir, &m, chunkConfig, opts.VaultPassphrase)
	if err != nil {
		return 0, fmt.Errorf("restore: materialize config tar: %w", err)
	}
	defer cfgCleanup()
	if cfgSrc != "" {
		// Config tar entries are tarPaths like "cometbft/config/genesis.json"
		// and "vault.key". We extract relative to dataDir, except vault.key
		// which goes to opts.VaultDestPath (default: parent of dataDir).
		vaultDest := opts.VaultDestPath
		if vaultDest == "" {
			vaultDest = filepath.Join(filepath.Dir(dataDir), "vault.key")
		}
		if err := untarZstdConfig(cfgSrc, dataDir, vaultDest); err != nil {
			return 0, fmt.Errorf("restore: untar config: %w", err)
		}
	}

	return m.Height, nil
}

// loadBadgerBackup creates a fresh BadgerDB at dst and loads the
// protobuf backup stream into it. Used both by Restore and by Verify's
// AppHash replay step.
func loadBadgerBackup(dst, backupPath string) error {
	opts := badger.DefaultOptions(dst)
	opts.Logger = nil
	db, err := badger.Open(opts)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	in, err := os.Open(backupPath) //nolint:gosec // backupPath is dir-derived
	if err != nil {
		_ = db.Close()
		return err
	}
	defer func() { _ = in.Close() }()
	if err := db.Load(in, 16); err != nil {
		_ = db.Close()
		return fmt.Errorf("load: %w", err)
	}
	return db.Close()
}

// atomicSwapDir replaces dst with src. If dst exists it's moved aside
// first; on success the moved-aside copy is removed. On failure dst
// is rolled back from the aside.
func atomicSwapDir(src, dst string) error {
	aside := dst + ".old-" + time.Now().Format("20060102-150405")
	if _, err := os.Stat(dst); err == nil {
		if err := os.Rename(dst, aside); err != nil {
			return fmt.Errorf("move aside: %w", err)
		}
	}
	if err := os.Rename(src, dst); err != nil {
		// Roll back: try to restore the aside.
		if _, asideErr := os.Stat(aside); asideErr == nil {
			_ = os.Rename(aside, dst)
		}
		return fmt.Errorf("rename src→dst: %w", err)
	}
	if _, err := os.Stat(aside); err == nil {
		_ = os.RemoveAll(aside)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // src is materialize() output
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func sanityOpenSQLite(path string) error {
	dsn := path + "?_busy_timeout=5000&mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return db.PingContext(ctx)
}

// untarZstd extracts every entry of a tar.zst into dstRoot. Entry
// names are sanitised against path traversal.
func untarZstd(src, dstRoot string) error {
	in, err := os.Open(src) //nolint:gosec // src is materialize() output
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	zr, err := zstd.NewReader(in)
	if err != nil {
		return fmt.Errorf("zstd: %w", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar next: %w", err)
		}
		// ZipSlip defence: reject any entry whose name isn't a local
		// relative path. filepath.IsLocal (Go 1.20+) is the canonical
		// safe-path predicate — it rejects absolute paths, "..",
		// reserved Windows names and empty components. We keep the
		// Clean + HasPrefix("..") + IsAbs belt-and-braces below as a
		// defensive double-check that also gives a clearer error.
		if !filepath.IsLocal(hdr.Name) {
			return fmt.Errorf("tar entry escapes root: %q", hdr.Name)
		}
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("tar entry escapes root: %q", hdr.Name)
		}
		target := filepath.Join(dstRoot, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA: //nolint:staticcheck // TypeRegA is deprecated alias; keep for cross-version tars
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil { //nolint:gosec // bounded by tar archive
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		default:
			// Symlinks and devices in a SAGE snapshot are not expected;
			// skip rather than fail to keep restore tolerant.
		}
	}
	return nil
}

// untarZstdConfig handles the config tarball, which has two
// destination conventions: cometbft/config/* goes under dataDir, and
// vault.key goes to vaultDest.
func untarZstdConfig(src, dataDir, vaultDest string) error {
	in, err := os.Open(src) //nolint:gosec // src is materialize() output
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	zr, err := zstd.NewReader(in)
	if err != nil {
		return fmt.Errorf("zstd: %w", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar next: %w", err)
		}
		// ZipSlip defence — see untarZstd above for rationale.
		if !filepath.IsLocal(hdr.Name) {
			return fmt.Errorf("tar entry escapes root: %q", hdr.Name)
		}
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("tar entry escapes root: %q", hdr.Name)
		}
		var target string
		switch clean {
		case "vault.key":
			target = vaultDest
		default:
			target = filepath.Join(dataDir, clean)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA: //nolint:staticcheck // legacy alias
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil { //nolint:gosec // bounded by tar archive
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
	return nil
}
