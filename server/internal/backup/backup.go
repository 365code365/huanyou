// Package backup creates and opens encrypted system backups.
//
// A bundle is a gzip'd tar containing:
//
//	manifest.json   what / when / from where
//	vpn.db          consistent SQLite snapshot (VACUUM INTO)
//	secrets.json    JWT secret, admin password, payment keys (so tokens and
//	                logins keep working after restore / clone)
//	releases/...    uploaded app packages (optional, can be large)
//
// Because it carries secrets, the bundle is always encrypted with a
// password: scrypt(N=2^15, r=8, p=1) derives a 256-bit key, and the stream is
// cut into 1 MiB chunks each sealed with AES-256-GCM. The chunk counter and a
// "last chunk" flag are bound into each nonce / AAD, so chunks can't be
// reordered, dropped or truncated without detection.
package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"
)

const (
	magic     = "HYBK1\n"
	chunkSize = 1 << 20
	saltLen   = 16
)

var ErrBadPassword = errors.New("备份密码错误，或文件已损坏")

// Manifest describes a bundle.
type Manifest struct {
	Format     int       `json:"format"` // 1
	CreatedAt  time.Time `json:"created_at"`
	PublicURL  string    `json:"public_url"`
	AppVersion string    `json:"app_version"`
	Users      int64     `json:"users"`
	Orders     int64     `json:"orders"`
	Nodes      int       `json:"nodes"`
	Packages   int       `json:"packages"` // release files included
	Note       string    `json:"note"`
}

// Secrets travels inside the encrypted bundle.
type Secrets struct {
	JWTSecret     string `json:"jwt_secret"`
	AdminPassword string `json:"admin_password"`
	CashierSign   string `json:"cashier_sign_key,omitempty"`
	CashierEnc    string `json:"cashier_enc_key,omitempty"`
	EpayKey       string `json:"epay_key,omitempty"`
}

// Input is what Write packs.
type Input struct {
	Manifest   Manifest
	Secrets    Secrets
	DBPath     string   // snapshot file to include as vpn.db
	ReleaseDir string   // directory with release packages
	Packages   []string // file names inside ReleaseDir to include
}

// Write encrypts a bundle with password into w.
func Write(w io.Writer, password string, in Input) error {
	if len(password) < 8 {
		return errors.New("备份密码至少 8 位")
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	key, err := deriveKey(password, salt)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, magic); err != nil {
		return err
	}
	if _, err := w.Write(salt); err != nil {
		return err
	}
	ew, err := newEncWriter(w, key)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(ew)
	tw := tar.NewWriter(gz)

	in.Manifest.Format = 1
	in.Manifest.Packages = len(in.Packages)
	addJSON := func(name string, v any) error {
		b, _ := json.MarshalIndent(v, "", "  ")
		return addBytes(tw, name, b)
	}
	if err := addJSON("manifest.json", in.Manifest); err != nil {
		return err
	}
	if err := addJSON("secrets.json", in.Secrets); err != nil {
		return err
	}
	if err := addFile(tw, "vpn.db", in.DBPath); err != nil {
		return err
	}
	for _, p := range in.Packages {
		if err := addFile(tw, "releases/"+p, filepath.Join(in.ReleaseDir, p)); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return ew.Close()
}

// Opened is an extracted bundle.
type Opened struct {
	Dir      string // temp dir; caller removes it
	Manifest Manifest
	Secrets  Secrets
	DBPath   string
	Packages []string // names under Dir/releases
}

// Open decrypts and extracts a bundle into a new directory under tmpRoot.
func Open(r io.Reader, password, tmpRoot string) (*Opened, error) {
	head := make([]byte, len(magic)+saltLen)
	if _, err := io.ReadFull(r, head); err != nil || string(head[:len(magic)]) != magic {
		return nil, errors.New("不是有效的系统备份文件")
	}
	key, err := deriveKey(password, head[len(magic):])
	if err != nil {
		return nil, err
	}
	dr, err := newDecReader(r, key)
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(dr)
	if err != nil {
		if errors.Is(err, ErrBadPassword) {
			return nil, ErrBadPassword
		}
		return nil, fmt.Errorf("备份内容损坏：%w", err)
	}
	dir, err := os.MkdirTemp(tmpRoot, "restore-")
	if err != nil {
		return nil, err
	}
	out := &Opened{Dir: dir}
	fail := func(err error) (*Opened, error) {
		os.RemoveAll(dir)
		if errors.Is(err, ErrBadPassword) {
			return nil, ErrBadPassword
		}
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fail(err)
		}
		name := filepath.Clean(h.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) || h.Typeflag != tar.TypeReg {
			continue
		}
		dst := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return fail(err)
		}
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return fail(err)
		}
		_, err = io.Copy(f, tr)
		f.Close()
		if err != nil {
			return fail(err)
		}
		switch {
		case name == "manifest.json":
			if err := readJSON(dst, &out.Manifest); err != nil {
				return fail(err)
			}
		case name == "secrets.json":
			if err := readJSON(dst, &out.Secrets); err != nil {
				return fail(err)
			}
		case name == "vpn.db":
			out.DBPath = dst
		case strings.HasPrefix(name, "releases/"):
			out.Packages = append(out.Packages, strings.TrimPrefix(name, "releases/"))
		}
	}
	if out.Manifest.Format != 1 || out.DBPath == "" {
		return fail(errors.New("备份文件不完整（缺少数据库）"))
	}
	return out, nil
}

func deriveKey(password string, salt []byte) ([]byte, error) {
	return scrypt.Key([]byte(password), salt, 1<<15, 8, 1, 32)
}

func addBytes(tw *tar.Writer, name string, b []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b)), ModTime: time.Now()}); err != nil {
		return err
	}
	_, err := tw.Write(b)
	return err
}

func addFile(tw *tar.Writer, name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: st.Size(), ModTime: st.ModTime()}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// ---- chunked AES-256-GCM ----
//
// Chunk on the wire: len(4, big-endian, ciphertext length) || ciphertext.
// nonce = prefix(4 random, written once after the salt) || counter(8)
// aad   = 1 byte: 1 for the final chunk, 0 otherwise.

type encWriter struct {
	w       io.Writer
	gcm     cipher.AEAD
	prefix  [4]byte
	counter uint64
	buf     bytes.Buffer
}

func newEncWriter(w io.Writer, key []byte) (*encWriter, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	e := &encWriter{w: w, gcm: gcm}
	if _, err := rand.Read(e.prefix[:]); err != nil {
		return nil, err
	}
	if _, err := w.Write(e.prefix[:]); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *encWriter) Write(p []byte) (int, error) {
	n := len(p)
	e.buf.Write(p)
	for e.buf.Len() > chunkSize { // keep at least one byte back for the final chunk
		if err := e.seal(e.buf.Next(chunkSize), false); err != nil {
			return 0, err
		}
	}
	return n, nil
}

func (e *encWriter) Close() error { return e.seal(e.buf.Bytes(), true) }

func (e *encWriter) seal(plain []byte, last bool) error {
	ct := e.gcm.Seal(nil, e.nonce(), plain, aad(last))
	e.counter++
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(ct)))
	if _, err := e.w.Write(l[:]); err != nil {
		return err
	}
	_, err := e.w.Write(ct)
	return err
}

func (e *encWriter) nonce() []byte {
	n := make([]byte, 12)
	copy(n, e.prefix[:])
	binary.BigEndian.PutUint64(n[4:], e.counter)
	return n
}

type decReader struct {
	r       io.Reader
	gcm     cipher.AEAD
	prefix  [4]byte
	counter uint64
	plain   []byte
	done    bool
}

func newDecReader(r io.Reader, key []byte) (*decReader, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	d := &decReader{r: r, gcm: gcm}
	if _, err := io.ReadFull(r, d.prefix[:]); err != nil {
		return nil, errors.New("备份文件已截断")
	}
	return d, nil
}

func (d *decReader) Read(p []byte) (int, error) {
	for len(d.plain) == 0 {
		if d.done {
			return 0, io.EOF
		}
		var l [4]byte
		if _, err := io.ReadFull(d.r, l[:]); err != nil {
			return 0, errors.New("备份文件已截断")
		}
		size := binary.BigEndian.Uint32(l[:])
		if size > chunkSize+64 {
			return 0, ErrBadPassword
		}
		ct := make([]byte, size)
		if _, err := io.ReadFull(d.r, ct); err != nil {
			return 0, errors.New("备份文件已截断")
		}
		nonce := make([]byte, 12)
		copy(nonce, d.prefix[:])
		binary.BigEndian.PutUint64(nonce[4:], d.counter)
		d.counter++
		// Try as a middle chunk, then as the final one.
		if pt, err := d.gcm.Open(nil, nonce, ct, aad(false)); err == nil {
			d.plain = pt
			continue
		}
		pt, err := d.gcm.Open(nil, nonce, ct, aad(true))
		if err != nil {
			return 0, ErrBadPassword
		}
		d.plain, d.done = pt, true
	}
	n := copy(p, d.plain)
	d.plain = d.plain[n:]
	return n, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func aad(last bool) []byte {
	if last {
		return []byte{1}
	}
	return []byte{0}
}
