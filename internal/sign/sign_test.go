package sign

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func testKeys(t *testing.T) (privPath, pubPath string, id string) {
	t.Helper()
	dir := t.TempDir()
	privPath = filepath.Join(dir, "repo.key")
	pubPath = filepath.Join(dir, "repo.pub")
	kp, err := WriteKeypair(privPath, pubPath)
	if err != nil {
		t.Fatal(err)
	}
	return privPath, pubPath, kp.KeyID
}

func TestSignVerify(t *testing.T) {
	_, pubPath, id := testKeys(t)
	priv, gotID, err := LoadPrivateKey(filepath.Join(filepath.Dir(pubPath), "repo.key"))
	if err != nil {
		t.Fatal(err)
	}
	if gotID != id {
		t.Fatalf("key ID = %s, want %s", gotID, id)
	}
	keys := NewKeyring()
	pub, pubID, err := LoadPublicKey(pubPath)
	if err != nil {
		t.Fatal(err)
	}
	keys.Keys[pubID] = pub
	keys.Paths[pubID] = pubPath

	data := []byte(`{"hello":"world"}`)
	sig, signer, err := SignBytes(priv, data)
	if err != nil {
		t.Fatal(err)
	}
	if signer != id {
		t.Fatalf("signer = %s, want %s", signer, id)
	}
	got, err := VerifyBytes(data, sig, keys)
	if err != nil {
		t.Fatalf("VerifyBytes: %v", err)
	}
	if got != id {
		t.Fatalf("verified key = %s, want %s", got, id)
	}
	if _, err := VerifyBytes(append(data, '!'), sig, keys); err == nil {
		t.Fatal("tampered data verified")
	}

	// A second key must not verify the first key's signature.
	kp2, err := WriteKeypair(filepath.Join(t.TempDir(), "other.key"), filepath.Join(t.TempDir(), "other.pub"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBytes(data, sig, keyringFor(t, kp2)); err != nil {
		// The signature names the original key ID, which is absent from this
		// keyring: that is the expected "not trusted" failure.
	} else {
		t.Fatal("signature verified with an unrelated keyring")
	}
}

func keyringFor(t *testing.T, kp *Keypair) *Keyring {
	t.Helper()
	kr := NewKeyring()
	kr.Keys[kp.KeyID] = kp.Public
	return kr
}

func TestSignFileAndTrustedDir(t *testing.T) {
	_, pubPath, id := testKeys(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "index.json")
	if err := os.WriteFile(file, []byte("signed bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	privPath := filepath.Join(filepath.Dir(pubPath), "repo.key")
	if _, err := SignFile(privPath, file); err != nil {
		t.Fatal(err)
	}
	trusted := filepath.Join(dir, "trusted.d")
	imported, dest, err := ImportPublicKey(trusted, pubPath)
	if err != nil {
		t.Fatal(err)
	}
	if imported != id || filepath.Base(dest) != id+".pub" {
		t.Fatalf("import = %s %s", imported, dest)
	}
	kr, err := LoadKeyring(trusted)
	if err != nil {
		t.Fatal(err)
	}
	if kr.Len() != 1 {
		t.Fatalf("keyring has %d keys", kr.Len())
	}
	got, err := VerifyFile(file, kr)
	if err != nil {
		t.Fatal(err)
	}
	if got != id {
		t.Fatalf("verified key = %s, want %s", got, id)
	}
	if _, err := RemovePublicKey(trusted, id[:12]); err != nil {
		t.Fatal(err)
	}
	if kr, _ := LoadKeyring(trusted); kr.Len() != 0 {
		t.Fatal("key was not removed")
	}
}

func TestPrivateKeyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions")
	}
	privPath, _, _ := testKeys(t)
	if err := os.Chmod(privPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadPrivateKey(privPath); err == nil {
		t.Fatal("world-readable private key accepted")
	}
}

func TestLoadKeyringRejectsBadKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.pub"), []byte("not a key"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeyring(dir); err == nil {
		t.Fatal("malformed trusted key accepted")
	}
}
