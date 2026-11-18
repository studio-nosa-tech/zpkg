package recipesig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/studio-nosa-tech/zpkg/internal/sign"
)

func testTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string, mode os.FileMode) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("core/hello/version", "1.0\n", 0o644)
	write("core/hello/build", "#!/bin/sh\necho hello\n", 0o755)
	write("main/tool/version", "2.0\n", 0o644)
	write("README.md", "recipes\n", 0o644)
	return root
}

func keypair(t *testing.T) (priv, pub string) {
	t.Helper()
	dir := t.TempDir()
	priv = filepath.Join(dir, "repo.key")
	pub = filepath.Join(dir, "repo.pub")
	if _, err := sign.WriteKeypair(priv, pub); err != nil {
		t.Fatal(err)
	}
	return priv, pub
}

func keyring(t *testing.T, pub string) *sign.Keyring {
	t.Helper()
	kr := sign.NewKeyring()
	p, id, err := sign.LoadPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	kr.Keys[id] = p
	kr.Paths[id] = pub
	return kr
}

func TestSignVerify(t *testing.T) {
	root := testTree(t)
	priv, pub := keypair(t)
	m, err := Sign(root, priv, 0, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if m.Generation != 1 || m.KeyID == "" || len(m.Files) != 4 {
		t.Fatalf("manifest = %+v", m)
	}
	v, err := Verify(root, keyring(t, pub), false, false)
	if err != nil {
		t.Fatal(err)
	}
	if v.Manifest.Generation != 1 || v.SHA256 == "" {
		t.Fatalf("verified = %+v", v)
	}
}

func TestModifyAndExtraFiles(t *testing.T) {
	root := testTree(t)
	priv, pub := keypair(t)
	if _, err := Sign(root, priv, 0, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	build := filepath.Join(root, "core/hello/build")
	if err := os.WriteFile(build, []byte("#!/bin/sh\necho pwned\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(root, keyring(t, pub), false, false); err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("modified file error = %v", err)
	}

	// Restore and add a file that the signed manifest does not cover.
	if _, err := Sign(root, priv, 0, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "core/evil/version"), []byte("1\n"), 0o644); err != nil {
		// The directory does not exist yet.
		if err := os.MkdirAll(filepath.Join(root, "core/evil"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "core/evil/version"), []byte("1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Verify(root, keyring(t, pub), false, false); err == nil || !strings.Contains(err.Error(), "not covered") {
		t.Fatalf("extra file error = %v", err)
	}
}

func TestMissingFile(t *testing.T) {
	root := testTree(t)
	priv, pub := keypair(t)
	if _, err := Sign(root, priv, 0, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "main/tool/version")); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(root, keyring(t, pub), false, false); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing file error = %v", err)
	}
}

func TestUnsignedAndExpiry(t *testing.T) {
	root := testTree(t)
	priv, pub := keypair(t)
	if _, err := Verify(root, keyring(t, pub), false, false); err != ErrUnsigned {
		t.Fatalf("unsigned error = %v, want ErrUnsigned", err)
	}
	if v, err := Verify(root, keyring(t, pub), true, false); err != nil || v != nil {
		t.Fatalf("allow unsigned = %v, %v", v, err)
	}

	if _, err := Sign(root, priv, 0, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(root, keyring(t, pub), false, false); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired error = %v", err)
	}
	if _, err := Verify(root, keyring(t, pub), false, true); err != nil {
		t.Fatalf("allow expired: %v", err)
	}
}

func TestInvalidSignatureNeverAllowedUnsigned(t *testing.T) {
	root := testTree(t)
	priv, pub := keypair(t)
	if _, err := Sign(root, priv, 0, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, SignatureName), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(root, keyring(t, pub), true, false); err == nil {
		t.Fatal("invalid signature was accepted with allowUnsigned")
	}
}

func TestRefuseKeyInsideTree(t *testing.T) {
	root := testTree(t)
	priv := filepath.Join(root, "repo.key")
	if _, err := sign.WriteKeypair(priv, filepath.Join(root, "repo.pub")); err != nil {
		t.Fatal(err)
	}
	if _, err := Sign(root, priv, 1, time.Now().Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "inside the recipe tree") {
		t.Fatalf("key inside tree error = %v", err)
	}
}
