package ollamad

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstallEngineTarGzVerifiesAndExtracts(t *testing.T) {
	archive := testTarGz(t, map[string]string{
		"ollama":             "#!/bin/sh\n",
		"lib/ollama/helper":  "helper",
		"nested/ignored.txt": "kept",
	})
	restore := stubEngineAsset(t, archive, "tar.gz")
	defer restore()

	m := New(t.TempDir())
	require.NoError(t, m.InstallEngine(context.Background(), nil))
	assert.True(t, m.EngineInstalled())
	assert.FileExists(t, filepath.Join(m.engineDir(), "lib", "ollama", "helper"))
}

func TestInstallEngineZipVerifiesAndExtracts(t *testing.T) {
	archive := testZip(t, map[string]string{
		"bin/ollama": "binary",
	})
	restore := stubEngineAsset(t, archive, "zip")
	defer restore()

	m := New(t.TempDir())
	require.NoError(t, m.InstallEngine(context.Background(), nil))
	assert.True(t, m.EngineInstalled())
	assert.FileExists(t, filepath.Join(m.engineDir(), "bin", "ollama"))
}

func TestInstallEngineChecksumMismatchRefuses(t *testing.T) {
	archive := testTarGz(t, map[string]string{"ollama": "binary"})
	restore := stubEngineAsset(t, archive, "tar.gz")
	defer restore()
	engineAssetFor = func() (engineAsset, bool) {
		return engineAsset{name: "ollama-test.tgz", sha256: "deadbeef", size: int64(len(archive)), format: "tar.gz"}, true
	}

	m := New(t.TempDir())
	err := m.InstallEngine(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksum mismatch")
	assert.False(t, m.EngineInstalled())
}

func stubEngineAsset(t *testing.T, payload []byte, format string) func() {
	t.Helper()
	oldBase, oldAssetFor := engineBaseURL, engineAssetFor
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	sum := sha256.Sum256(payload)
	engineBaseURL = srv.URL + "/"
	engineAssetFor = func() (engineAsset, bool) {
		return engineAsset{
			name:   "ollama-test",
			sha256: hex.EncodeToString(sum[:]),
			size:   int64(len(payload)),
			format: format,
		}, true
	}
	return func() {
		engineBaseURL = oldBase
		engineAssetFor = oldAssetFor
		srv.Close()
	}
}

func testTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		mode := int64(0o644)
		if filepath.Base(name) == "ollama" {
			mode = 0o755
		}
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body))}))
		_, err := tw.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func testZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(os.FileMode(0o755))
		w, err := zw.CreateHeader(h)
		require.NoError(t, err)
		_, err = w.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// A tar containing symlinks whose targets escape the extract root must not create
// those links (arbitrary file write / go/unsafe-unzip-symlink), while a safe internal
// symlink is still extracted.
func TestExtractTar_RejectsEscapingSymlinks(t *testing.T) {
	dst := t.TempDir()
	outside := filepath.Join(t.TempDir(), "victim")

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	content := []byte("x")
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "ok.txt", Typeflag: tar.TypeReg, Size: int64(len(content)), Mode: 0o644}))
	_, _ = tw.Write(content)
	writeSym := func(name, target string) {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeSymlink, Linkname: target, Mode: 0o777}))
	}
	writeSym("escape-rel", "../../../../../../etc/passwd")
	writeSym("escape-abs", "/etc/passwd")
	writeSym("safe", "ok.txt")
	require.NoError(t, tw.Close())

	require.NoError(t, extractTar(&buf, dst))

	for _, n := range []string{"escape-rel", "escape-abs"} {
		_, err := os.Lstat(filepath.Join(dst, n))
		assert.True(t, os.IsNotExist(err), "escaping symlink %q must be skipped", n)
	}
	fi, err := os.Lstat(filepath.Join(dst, "safe"))
	require.NoError(t, err)
	assert.True(t, fi.Mode()&os.ModeSymlink != 0, "safe internal symlink should be created")
	_, err = os.Stat(outside)
	assert.True(t, os.IsNotExist(err), "nothing must be written outside the extract root")
}

func TestArchiveTargetWithinRoot(t *testing.T) {
	root := filepath.Clean("/x/root")
	assert.True(t, archiveTargetWithinRoot(root, filepath.Join(root, "a"), "b"))
	assert.True(t, archiveTargetWithinRoot(root, root, "sub/file"))
	assert.False(t, archiveTargetWithinRoot(root, filepath.Join(root, "a"), "../../etc"))
	assert.False(t, archiveTargetWithinRoot(root, root, "../sibling"))
	assert.False(t, archiveTargetWithinRoot(root, root, "/etc/passwd"))
	assert.False(t, archiveTargetWithinRoot(root, root, ""))
}
