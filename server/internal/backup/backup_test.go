package backup

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

func writeTestBundle(t *testing.T, pkgSize int) ([]byte, []byte) {
	t.Helper()
	dir := t.TempDir()
	db := filepath.Join(dir, "vpn.db")
	dbData := bytes.Repeat([]byte("sqlite-page "), 50_000) // ~600 KB
	os.WriteFile(db, dbData, 0o600)
	rel := filepath.Join(dir, "releases")
	os.MkdirAll(rel, 0o700)
	pkg := make([]byte, pkgSize)
	rand.Read(pkg) // incompressible, spans several chunks
	os.WriteFile(filepath.Join(rel, "huanyou-android-1.0.0-1.apk"), pkg, 0o600)

	var buf bytes.Buffer
	err := Write(&buf, "correct horse", Input{
		Manifest:   Manifest{PublicURL: "https://a.example.com", Users: 3},
		Secrets:    Secrets{JWTSecret: "jwt-secret-value", AdminPassword: "pw"},
		DBPath:     db,
		ReleaseDir: rel,
		Packages:   []string{"huanyou-android-1.0.0-1.apk"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), pkg
}

func TestRoundTrip(t *testing.T) {
	b, pkg := writeTestBundle(t, 3<<20+123)
	if bytes.Contains(b, []byte("jwt-secret-value")) {
		t.Fatal("secrets visible in the bundle")
	}
	o, err := Open(bytes.NewReader(b), "correct horse", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(o.Dir)
	if o.Secrets.JWTSecret != "jwt-secret-value" || o.Manifest.Users != 3 || o.Manifest.Packages != 1 {
		t.Fatalf("bad contents: %+v %+v", o.Manifest, o.Secrets)
	}
	got, _ := os.ReadFile(filepath.Join(o.Dir, "releases", o.Packages[0]))
	if !bytes.Equal(got, pkg) {
		t.Fatal("package corrupted")
	}
}

func TestRejects(t *testing.T) {
	b, _ := writeTestBundle(t, 2<<20)
	if _, err := Open(bytes.NewReader(b), "wrong password", t.TempDir()); err != ErrBadPassword {
		t.Fatalf("wrong password: %v", err)
	}
	// Truncation: drop the final chunk.
	if _, err := Open(bytes.NewReader(b[:len(b)-200]), "correct horse", t.TempDir()); err == nil {
		t.Fatal("truncated bundle accepted")
	}
	// Bit flip in the middle.
	bad := append([]byte(nil), b...)
	bad[len(bad)/2] ^= 0x40
	if _, err := Open(bytes.NewReader(bad), "correct horse", t.TempDir()); err == nil {
		t.Fatal("tampered bundle accepted")
	}
	if _, err := Open(bytes.NewReader([]byte("not a backup at all")), "x", t.TempDir()); err == nil {
		t.Fatal("garbage accepted")
	}
}
