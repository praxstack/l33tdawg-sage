package ollamad

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/klauspost/compress/zstd"
)

const engineRelease = "v0.31.1"

type engineAsset struct {
	name   string
	sha256 string
	size   int64
	format string // "tar.gz" | "tar.zst" | "zip"
}

var engineAssets = map[string]engineAsset{
	"darwin/amd64":  {"ollama-darwin.tgz", "0c4f92389fcc1f651c17282e2eaffd68c8d3d06e1f7b307604102ad0e09a10c9", 129037451, "tar.gz"},
	"darwin/arm64":  {"ollama-darwin.tgz", "0c4f92389fcc1f651c17282e2eaffd68c8d3d06e1f7b307604102ad0e09a10c9", 129037451, "tar.gz"},
	"linux/amd64":   {"ollama-linux-amd64.tar.zst", "d297381efc136451f6fabb9dd644a67f70fe51c16815a0c4a95ff0e327a3afb4", 1408625102, "tar.zst"},
	"linux/arm64":   {"ollama-linux-arm64.tar.zst", "47c82a67e59e060a735d1cb50a2acf020126a3a4be3f6847d5b58b7dd59620b6", 1528984840, "tar.zst"},
	"windows/amd64": {"ollama-windows-amd64.zip", "9ecf5a631561c7dff3a143925f11e2008327be738a7279fcf0c5462b9c422700", 1497028570, "zip"},
	"windows/arm64": {"ollama-windows-arm64.zip", "f529ac520435fba895f652922ef0dc7f1be1b951bcea5eaf360701c06d3f5e82", 16171764, "zip"},
}

const maxExtractedBytes int64 = 8 << 30

var (
	engineBaseURL  = "https://github.com/ollama/ollama/releases/download/" + engineRelease + "/"
	engineAssetFor = func() (engineAsset, bool) {
		a, ok := engineAssets[runtime.GOOS+"/"+runtime.GOARCH]
		return a, ok
	}
)

func (m *Manager) InstallSupported() bool {
	_, ok := engineAssetFor()
	return ok
}

func (m *Manager) EngineSizeBytes() int64 {
	a, ok := engineAssetFor()
	if !ok {
		return 0
	}
	return a.size
}

func (m *Manager) InstallEngine(ctx context.Context, progress func(done, total int64)) error {
	if m.EngineInstalled() {
		return nil
	}
	m.dlMu.Lock()
	if m.installing {
		m.dlMu.Unlock()
		return fmt.Errorf("an Ollama runtime install is already in progress")
	}
	m.installing = true
	m.dlDone, m.dlTotal = 0, m.EngineSizeBytes()
	m.dlMu.Unlock()
	defer func() {
		m.dlMu.Lock()
		m.installing = false
		m.dlMu.Unlock()
	}()
	asset, ok := engineAssetFor()
	if !ok {
		return fmt.Errorf("no pinned Ollama runtime for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if err := os.MkdirAll(m.dataDir, 0o755); err != nil {
		return err
	}
	tmpArchive := filepath.Join(m.dataDir, asset.name+".part")
	f, err := os.Create(tmpArchive)
	if err != nil {
		return fmt.Errorf("create temp archive: %w", err)
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmpArchive)
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, engineBaseURL+asset.name, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 0}).Do(req)
	if err != nil {
		return fmt.Errorf("download Ollama runtime: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download Ollama runtime: http %d", resp.StatusCode)
	}
	hasher := sha256.New()
	var done int64
	buf := make([]byte, 1<<20)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if _, writeErr := f.Write(chunk); writeErr != nil {
				return fmt.Errorf("write runtime archive: %w", writeErr)
			}
			_, _ = hasher.Write(chunk)
			done += int64(n)
			if done > asset.size {
				return fmt.Errorf("ollama runtime archive larger than pinned size - refusing it")
			}
			m.dlMu.Lock()
			m.dlDone = done
			m.dlMu.Unlock()
			if progress != nil {
				progress(done, asset.size)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("download Ollama runtime: %w", rerr)
		}
	}
	if done != asset.size {
		return fmt.Errorf("ollama runtime download incomplete: got %d of %d bytes", done, asset.size)
	}
	if sum := hex.EncodeToString(hasher.Sum(nil)); sum != asset.sha256 {
		return fmt.Errorf("ollama runtime checksum mismatch (got %s) - refusing to install it", sum)
	}
	if closeErr := f.Close(); closeErr != nil {
		return fmt.Errorf("close runtime archive: %w", closeErr)
	}

	tmpDir := m.engineDir() + ".part"
	_ = os.RemoveAll(tmpDir)
	if mkdirErr := os.MkdirAll(tmpDir, 0o755); mkdirErr != nil {
		return fmt.Errorf("create runtime dir: %w", mkdirErr)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	switch asset.format {
	case "tar.gz":
		err = extractTarGz(tmpArchive, tmpDir)
	case "tar.zst":
		err = extractTarZst(tmpArchive, tmpDir)
	case "zip":
		err = extractZip(tmpArchive, tmpDir)
	default:
		err = fmt.Errorf("unknown archive format %q", asset.format)
	}
	if err != nil {
		return fmt.Errorf("extract Ollama runtime: %w", err)
	}
	if !binaryExistsIn(tmpDir) {
		return fmt.Errorf("ollama runtime archive did not contain %s", binaryName)
	}
	if m.EngineInstalled() {
		return nil
	}
	_ = os.RemoveAll(m.engineDir())
	if err := os.Rename(tmpDir, m.engineDir()); err != nil {
		return fmt.Errorf("install Ollama runtime: %w", err)
	}
	return nil
}

func binaryExistsIn(root string) bool {
	exe := binaryName
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	for _, p := range []string{
		filepath.Join(root, exe),
		filepath.Join(root, "bin", exe),
		filepath.Join(root, "ollama", exe),
	} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}

func extractTarGz(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	return extractTar(gz, dst)
}

func extractTarZst(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := zstd.NewReader(f)
	if err != nil {
		return err
	}
	defer zr.Close()
	return extractTar(zr, dst)
}

func extractTar(r io.Reader, dst string) error {
	tr := tar.NewReader(r)
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name, ok := safeArchiveName(hdr.Name)
		if !ok {
			continue
		}
		path, err := safeArchivePath(dst, name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += hdr.Size
			if total > maxExtractedBytes {
				return fmt.Errorf("archive expands past the %dGB safety cap", maxExtractedBytes>>30)
			}
			mode := os.FileMode(0o644)
			if hdr.FileInfo().Mode()&0o111 != 0 {
				mode = 0o755
			}
			if err := writeLimitedFile(path, tr, hdr.Size, mode); err != nil {
				return err
			}
		case tar.TypeSymlink, tar.TypeLink:
			target := filepath.Clean(hdr.Linkname)
			if filepath.IsAbs(target) || strings.HasPrefix(target, "..") {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			_ = os.Symlink(target, path)
		}
	}
}

func extractZip(src, dst string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()
	var total int64
	for _, f := range zr.File {
		name, ok := safeArchiveName(f.Name)
		if !ok {
			continue
		}
		path, err := safeArchivePath(dst, name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if mkdirErr := os.MkdirAll(path, 0o755); mkdirErr != nil {
				return mkdirErr
			}
			continue
		}
		if !f.Mode().IsRegular() {
			continue
		}
		total += int64(f.UncompressedSize64)
		if total > maxExtractedBytes {
			return fmt.Errorf("archive expands past the %dGB safety cap", maxExtractedBytes>>30)
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if f.Mode()&0o111 != 0 || strings.HasSuffix(strings.ToLower(name), ".exe") {
			mode = 0o755
		}
		err = writeLimitedFile(path, rc, int64(f.UncompressedSize64), mode)
		_ = rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func safeArchiveName(raw string) (string, bool) {
	clean := filepath.Clean(raw)
	if clean == "." || clean == ".." || filepath.IsAbs(clean) ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) ||
		strings.HasPrefix(clean, ".git"+string(filepath.Separator)) {
		return "", false
	}
	return clean, true
}

func safeArchivePath(dst, name string) (string, error) {
	if _, ok := safeArchiveName(name); !ok {
		return "", fmt.Errorf("unsafe archive entry %q", name)
	}
	root := filepath.Clean(dst)
	path := filepath.Join(root, name)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || filepath.IsAbs(rel) ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry escapes destination: %s", name)
	}
	return path, nil
}

func writeLimitedFile(path string, r io.Reader, size int64, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	n, cerr := io.Copy(f, io.LimitReader(r, size+1))
	if err := f.Close(); err != nil {
		return err
	}
	if cerr != nil {
		return cerr
	}
	if n > size {
		return fmt.Errorf("%s: content exceeds declared size", filepath.Base(path))
	}
	return nil
}
