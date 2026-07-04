package rerankd

import (
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

// withFakeAsset points the download seams at srv serving `payload` and
// restores production values afterwards.
func withFakeAsset(t *testing.T, payload []byte, sha string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	prevURL, prevSHA, prevSize := modelSrcURL, modelWantSHA, modelWantSize
	modelSrcURL, modelWantSHA, modelWantSize = srv.URL, sha, int64(len(payload))
	t.Cleanup(func() {
		modelSrcURL, modelWantSHA, modelWantSize = prevURL, prevSHA, prevSize
		srv.Close()
	})
	return srv
}

func TestDownload_VerifiesAndInstalls(t *testing.T) {
	payload := []byte("not really a gguf but exactly these bytes")
	sum := sha256.Sum256(payload)
	withFakeAsset(t, payload, hex.EncodeToString(sum[:]))

	m := New(t.TempDir())
	require.False(t, m.ModelReady())

	var lastDone, lastTotal int64
	err := m.Download(context.Background(), func(done, total int64) { lastDone, lastTotal = done, total })
	require.NoError(t, err)
	assert.True(t, m.ModelReady())
	assert.Equal(t, int64(len(payload)), lastDone)
	assert.Equal(t, int64(len(payload)), lastTotal)

	// Idempotent: a second call is a no-op success.
	require.NoError(t, m.Download(context.Background(), nil))

	// The temp .part file must be gone.
	_, err = os.Stat(m.ModelPath() + ".part")
	assert.True(t, os.IsNotExist(err))
}

func TestDownload_RejectsChecksumMismatch(t *testing.T) {
	payload := []byte("tampered payload bytes")
	withFakeAsset(t, payload, "0000000000000000000000000000000000000000000000000000000000000000")

	m := New(t.TempDir())
	err := m.Download(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksum mismatch")
	assert.False(t, m.ModelReady())
	// Nothing half-installed.
	_, statErr := os.Stat(m.ModelPath())
	assert.True(t, os.IsNotExist(statErr))
}

func TestDownload_RejectsTruncatedBody(t *testing.T) {
	payload := []byte("full payload the server will truncate")
	sum := sha256.Sum256(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload[:10]) // short write
	}))
	defer srv.Close()
	prevURL, prevSHA, prevSize := modelSrcURL, modelWantSHA, modelWantSize
	modelSrcURL, modelWantSHA, modelWantSize = srv.URL, hex.EncodeToString(sum[:]), int64(len(payload))
	t.Cleanup(func() { modelSrcURL, modelWantSHA, modelWantSize = prevURL, prevSHA, prevSize })

	m := New(t.TempDir())
	err := m.Download(context.Background(), nil)
	require.Error(t, err)
	assert.False(t, m.ModelReady())
}

func TestModelPathUnderDataDir(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)
	assert.Equal(t, filepath.Join(dir, "models", modelFileName), m.ModelPath())
	assert.Contains(t, m.URL(), "127.0.0.1")
}
