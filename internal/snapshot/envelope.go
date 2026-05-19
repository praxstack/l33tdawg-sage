package snapshot

// envelope.go implements the at-rest crypto for snapshot chunks. The
// envelope is the same Argon2id + AES-256-GCM construction used in
// internal/tlsca/manifest_crypt.go (v6.8.0). We chunk the plaintext so
// individual stream writes can be GCM-sealed without buffering the
// entire chunk in memory — important for multi-GB BadgerDB backups.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters MUST match internal/tlsca/manifest_crypt.go so a
// future shared helper can replace both call sites without an envelope
// version bump.
const (
	envArgonTime    = 3
	envArgonMemory  = 64 * 1024 // 64 MiB
	envArgonThreads = 4
	envKeyLen       = 32
	envSaltLen      = 16
	envNonceLen     = 12
	envMagic        = "SAGEENV1"
	envChunkSize    = 1 << 20 // 1 MiB plaintext blocks
)

// envelopeHeader is the leading record of an encrypted chunk:
//
//	magic[8] | salt[16] | <stream of frames>
//
// Each frame is:
//
//	uint32(big-endian) length | nonce[12] | ciphertext (length bytes)
//
// EOF after a frame ends the stream cleanly. Truncation is detected by
// the absence of a closing zero-length frame written by envelopeWriter.Close.

// envelopeWriter wraps an underlying io.WriteCloser and chunk-seals
// plaintext as it's written. It is safe to call Write with any size;
// the writer buffers up to envChunkSize before emitting a frame.
type envelopeWriter struct {
	w      io.Writer
	gcm    cipher.AEAD
	buf    []byte
	err    error
	closed bool
}

func newEnvelopeWriter(w io.Writer, passphrase string) (*envelopeWriter, error) {
	if passphrase == "" {
		return nil, errors.New("envelope: empty passphrase")
	}
	salt := make([]byte, envSaltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("envelope: salt: %w", err)
	}
	key := argon2.IDKey([]byte(passphrase), salt,
		envArgonTime, envArgonMemory, envArgonThreads, envKeyLen)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("envelope: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("envelope: gcm: %w", err)
	}

	// Header: magic + salt. Read-side recovers salt → key from passphrase.
	if _, err := w.Write([]byte(envMagic)); err != nil {
		return nil, fmt.Errorf("envelope: write magic: %w", err)
	}
	if _, err := w.Write(salt); err != nil {
		return nil, fmt.Errorf("envelope: write salt: %w", err)
	}
	return &envelopeWriter{w: w, gcm: gcm, buf: make([]byte, 0, envChunkSize)}, nil
}

func (e *envelopeWriter) Write(p []byte) (int, error) {
	if e.err != nil {
		return 0, e.err
	}
	written := 0
	for len(p) > 0 {
		space := envChunkSize - len(e.buf)
		take := len(p)
		if take > space {
			take = space
		}
		e.buf = append(e.buf, p[:take]...)
		p = p[take:]
		written += take
		if len(e.buf) >= envChunkSize {
			if err := e.flushFrame(); err != nil {
				e.err = err
				return written, err
			}
		}
	}
	return written, nil
}

func (e *envelopeWriter) flushFrame() error {
	if len(e.buf) == 0 {
		return nil
	}
	nonce := make([]byte, envNonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("envelope: nonce: %w", err)
	}
	ct := e.gcm.Seal(nil, nonce, e.buf, nil)
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(ct))) //nolint:gosec // bounded by envChunkSize
	if _, err := e.w.Write(lenBuf[:]); err != nil {
		return err
	}
	if _, err := e.w.Write(nonce); err != nil {
		return err
	}
	if _, err := e.w.Write(ct); err != nil {
		return err
	}
	e.buf = e.buf[:0]
	return nil
}

// Close flushes any trailing partial frame, then writes a zero-length
// terminator frame so the reader can distinguish clean EOF from
// truncation. It does NOT close the underlying writer — callers own
// that lifecycle.
func (e *envelopeWriter) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	if e.err != nil {
		return e.err
	}
	if err := e.flushFrame(); err != nil {
		return err
	}
	// Terminator: 4-byte zero length, no nonce, no ciphertext.
	var lenBuf [4]byte
	if _, err := e.w.Write(lenBuf[:]); err != nil {
		return err
	}
	return nil
}

// envelopeReader inverts envelopeWriter. It exposes plaintext via Read.
type envelopeReader struct {
	r   io.Reader
	gcm cipher.AEAD
	buf []byte // remaining plaintext from the last decoded frame
	eof bool
}

func newEnvelopeReader(r io.Reader, passphrase string) (*envelopeReader, error) {
	magic := make([]byte, len(envMagic))
	if _, err := io.ReadFull(r, magic); err != nil {
		return nil, fmt.Errorf("envelope: read magic: %w", err)
	}
	if string(magic) != envMagic {
		return nil, errors.New("envelope: bad magic")
	}
	salt := make([]byte, envSaltLen)
	if _, err := io.ReadFull(r, salt); err != nil {
		return nil, fmt.Errorf("envelope: read salt: %w", err)
	}
	key := argon2.IDKey([]byte(passphrase), salt,
		envArgonTime, envArgonMemory, envArgonThreads, envKeyLen)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("envelope: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("envelope: gcm: %w", err)
	}
	return &envelopeReader{r: r, gcm: gcm}, nil
}

func (e *envelopeReader) Read(p []byte) (int, error) {
	if len(e.buf) > 0 {
		n := copy(p, e.buf)
		e.buf = e.buf[n:]
		return n, nil
	}
	if e.eof {
		return 0, io.EOF
	}
	// Read next frame header.
	var lenBuf [4]byte
	if _, err := io.ReadFull(e.r, lenBuf[:]); err != nil {
		return 0, err
	}
	ctLen := binary.BigEndian.Uint32(lenBuf[:])
	if ctLen == 0 {
		e.eof = true
		return 0, io.EOF
	}
	nonce := make([]byte, envNonceLen)
	if _, err := io.ReadFull(e.r, nonce); err != nil {
		return 0, fmt.Errorf("envelope: read nonce: %w", err)
	}
	ct := make([]byte, ctLen)
	if _, err := io.ReadFull(e.r, ct); err != nil {
		return 0, fmt.Errorf("envelope: read ct: %w", err)
	}
	pt, err := e.gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return 0, fmt.Errorf("envelope: open: %w", err)
	}
	e.buf = pt
	n := copy(p, e.buf)
	e.buf = e.buf[n:]
	return n, nil
}

// encryptFileInPlace reads src plaintext and writes the encrypted
// envelope to dst. Caller is responsible for removing src on success.
func encryptFileInPlace(src, dst, passphrase string) error {
	in, err := os.Open(src) //nolint:gosec // src is staging path
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	ew, err := newEnvelopeWriter(out, passphrase)
	if err != nil {
		return err
	}
	if _, err := io.Copy(ew, in); err != nil {
		return err
	}
	if err := ew.Close(); err != nil {
		return err
	}
	return out.Sync()
}

// hashPlaintextEncryptedFile computes SHA-256 over the decrypted
// plaintext of an envelope file. Verify uses this to compare to the
// manifest's recorded hash, which is always over plaintext (so the
// encryption posture can change without rotating manifests).
func hashPlaintextEncryptedFile(path, passphrase string) (Chunk, error) {
	in, err := os.Open(path) //nolint:gosec // path is staging-derived
	if err != nil {
		return Chunk{}, err
	}
	defer func() { _ = in.Close() }()
	er, err := newEnvelopeReader(in, passphrase)
	if err != nil {
		return Chunk{}, err
	}
	h := sha256.New()
	n, err := io.Copy(h, er)
	if err != nil {
		return Chunk{}, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return Chunk{}, err
	}
	_ = n
	return Chunk{
		Name:   "", // caller sets relative name
		SHA256: hex.EncodeToString(h.Sum(nil)),
		Size:   st.Size(),
	}, nil
}
