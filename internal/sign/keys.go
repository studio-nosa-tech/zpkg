// Package sign implements Ed25519 signing and verification for zpkg
// repository metadata. Keys are plain PEM files: PKCS#8 for private keys and
// PKIX SubjectPublicKeyInfo for public keys. The key ID is the SHA-256 of the
// raw 32-byte public key in lowercase hex.
package sign

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const (
	// PrivatePEM is the PEM type written by WriteKeypair.
	PrivatePEM = "PRIVATE KEY"
	// PublicPEM is the PEM type of trusted repository keys.
	PublicPEM = "PUBLIC KEY"
)

// Keypair is a generated Ed25519 key pair and its key ID.
type Keypair struct {
	Private ed25519.PrivateKey
	Public  ed25519.PublicKey
	KeyID   string
}

// KeyID returns the stable identifier of a public key: the SHA-256 of the
// raw key bytes as lowercase hex (64 characters).
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

// GenerateKeypair creates a new Ed25519 key pair.
func GenerateKeypair() (*Keypair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Keypair{Private: priv, Public: pub, KeyID: KeyID(pub)}, nil
}

// WriteKeypair writes a new key pair to privPath and pubPath. Both files are
// created exclusively (an existing file is an error) and the private key is
// mode 0600. If the second write fails the first file is removed so a partial
// key pair is never left behind.
func WriteKeypair(privPath, pubPath string) (*Keypair, error) {
	kp, err := GenerateKeypair()
	if err != nil {
		return nil, err
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(kp.Private)
	if err != nil {
		return nil, err
	}
	pubDER, err := x509.MarshalPKIXPublicKey(kp.Public)
	if err != nil {
		return nil, err
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: PrivatePEM, Bytes: privDER})
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: PublicPEM, Bytes: pubDER})
	if err := writeExclusive(privPath, privPEM, 0o600); err != nil {
		return nil, err
	}
	if err := writeExclusive(pubPath, pubPEM, 0o644); err != nil {
		_ = os.Remove(privPath)
		return nil, err
	}
	return kp, nil
}

func writeExclusive(path string, data []byte, perm os.FileMode) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

// EncodePublicKey returns the PKIX PEM representation of pub.
func EncodePublicKey(pub ed25519.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: PublicPEM, Bytes: der}), nil
}

// LoadPrivateKey reads a PKCS#8 Ed25519 private key. On Unix a key readable by
// group or others is rejected: a signing key must stay private.
func LoadPrivateKey(path string) (ed25519.PrivateKey, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, "", fmt.Errorf("%s: private key permissions %04o are too open (chmod 600)", path, info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != PrivatePEM {
		return nil, "", fmt.Errorf("%s: not a PEM %q block", path, PrivatePEM)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", path, err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, "", fmt.Errorf("%s: not an Ed25519 private key", path)
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, "", fmt.Errorf("%s: cannot derive Ed25519 public key", path)
	}
	return priv, KeyID(pub), nil
}

// LoadPublicKey reads a PKIX Ed25519 public key and returns it with its key ID.
func LoadPublicKey(path string) (ed25519.PublicKey, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != PublicPEM {
		return nil, "", fmt.Errorf("%s: not a PEM %q block", path, PublicPEM)
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", path, err)
	}
	pub, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, "", fmt.Errorf("%s: not an Ed25519 public key", path)
	}
	return pub, KeyID(pub), nil
}

// KeyInfo describes one trusted public key.
type KeyInfo struct {
	ID   string
	Path string
}

// Keyring is the set of trusted public keys loaded from a directory.
type Keyring struct {
	Keys  map[string]ed25519.PublicKey
	Paths map[string]string
}

// NewKeyring returns an empty keyring.
func NewKeyring() *Keyring {
	return &Keyring{Keys: map[string]ed25519.PublicKey{}, Paths: map[string]string{}}
}

// Len reports how many keys are trusted.
func (k *Keyring) Len() int {
	if k == nil {
		return 0
	}
	return len(k.Keys)
}

// List returns the trusted keys sorted by key ID.
func (k *Keyring) List() []KeyInfo {
	if k == nil {
		return nil
	}
	out := make([]KeyInfo, 0, len(k.Keys))
	for id, path := range k.Paths {
		out = append(out, KeyInfo{ID: id, Path: path})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// LoadKeyring reads every *.pub file in dir. A missing directory is an empty
// keyring; a malformed key is an error so a typo cannot silently disable
// verification.
func LoadKeyring(dir string) (*Keyring, error) {
	kr := NewKeyring()
	items, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return kr, nil
		}
		return nil, err
	}
	for _, it := range items {
		if it.IsDir() || !strings.HasSuffix(it.Name(), ".pub") {
			continue
		}
		path := filepath.Join(dir, it.Name())
		pub, id, err := LoadPublicKey(path)
		if err != nil {
			return nil, err
		}
		if _, dup := kr.Keys[id]; dup {
			continue
		}
		kr.Keys[id] = pub
		kr.Paths[id] = path
	}
	return kr, nil
}

// ImportPublicKey validates pubPath and installs it as <trustedDir>/<keyid>.pub.
func ImportPublicKey(trustedDir, pubPath string) (string, string, error) {
	pub, id, err := LoadPublicKey(pubPath)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(trustedDir, 0o755); err != nil {
		return "", "", err
	}
	dest := filepath.Join(trustedDir, id+".pub")
	if existing, existingID, err := LoadPublicKey(dest); err == nil {
		if existingID == id && bytes.Equal(existing, pub) {
			return id, dest, nil
		}
		return "", "", fmt.Errorf("%s already exists with different content", dest)
	}
	data, err := os.ReadFile(pubPath)
	if err != nil {
		return "", "", err
	}
	if err := writeFileAtomic(dest, data, 0o644); err != nil {
		return "", "", err
	}
	return id, dest, nil
}

// RemovePublicKey removes a trusted key by full key ID or an unambiguous
// prefix. It returns the removed key ID.
func RemovePublicKey(trustedDir, idOrPrefix string) (string, error) {
	kr, err := LoadKeyring(trustedDir)
	if err != nil {
		return "", err
	}
	if idOrPrefix == "" {
		return "", fmt.Errorf("empty key ID")
	}
	var matches []string
	for _, info := range kr.List() {
		if info.ID == idOrPrefix {
			matches = []string{info.ID}
			break
		}
		if strings.HasPrefix(info.ID, idOrPrefix) {
			matches = append(matches, info.ID)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no trusted key matches %q", idOrPrefix)
	case 1:
	default:
		return "", fmt.Errorf("key ID prefix %q is ambiguous (%d keys)", idOrPrefix, len(matches))
	}
	path := kr.Paths[matches[0]]
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return matches[0], nil
}

// writeFileAtomic writes data through a temporary file in the same directory
// and renames it into place, so readers never see a truncated file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}
