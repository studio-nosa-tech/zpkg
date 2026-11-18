package sign

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
)

// Signature schema and algorithm identifiers. The algorithm field leaves
// room for another signature backend (for example OpenPGP) without changing
// the file name convention.
const (
	SigSchema        = 1
	AlgorithmEd25519 = "ed25519"
)

// Envelope is the JSON detached signature written next to a signed file.
type Envelope struct {
	Schema    int    `json:"schema"`
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"keyid"`
	Signature string `json:"signature"`
}

// SignBytes signs data and returns the JSON signature envelope.
func SignBytes(priv ed25519.PrivateKey, data []byte) ([]byte, string, error) {
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, "", fmt.Errorf("not an Ed25519 private key")
	}
	id := KeyID(pub)
	env := Envelope{
		Schema:    SigSchema,
		Algorithm: AlgorithmEd25519,
		KeyID:     id,
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data)),
	}
	out, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return nil, "", err
	}
	return append(out, '\n'), id, nil
}

// VerifyBytes verifies a detached signature against data using the trusted
// keyring. It returns the signing key ID.
func VerifyBytes(data, sigData []byte, keys *Keyring) (string, error) {
	var env Envelope
	if err := json.Unmarshal(sigData, &env); err != nil {
		return "", fmt.Errorf("invalid signature file: %w", err)
	}
	if env.Schema != SigSchema {
		return "", fmt.Errorf("unsupported signature schema %d", env.Schema)
	}
	if env.Algorithm != AlgorithmEd25519 {
		return "", fmt.Errorf("unsupported signature algorithm %q", env.Algorithm)
	}
	if len(env.KeyID) != 64 {
		return "", fmt.Errorf("invalid signature key ID")
	}
	sig, err := base64.StdEncoding.DecodeString(env.Signature)
	if err != nil {
		return "", fmt.Errorf("invalid signature encoding: %w", err)
	}
	if len(sig) != ed25519.SignatureSize {
		return "", fmt.Errorf("invalid Ed25519 signature length %d", len(sig))
	}
	if keys == nil {
		return "", fmt.Errorf("signature key %s is not trusted (no trusted keys)", env.KeyID)
	}
	pub, ok := keys.Keys[env.KeyID]
	if !ok {
		return "", fmt.Errorf("signature key %s is not trusted", env.KeyID)
	}
	if !ed25519.Verify(pub, data, sig) {
		return "", fmt.Errorf("signature verification failed (key %s)", env.KeyID)
	}
	return env.KeyID, nil
}

// SignaturePath returns the detached signature path for file.
func SignaturePath(file string) string { return file + ".sig" }

// SignFile signs file and writes file+".sig". The private key is read from
// privPath.
func SignFile(privPath, file string) (string, error) {
	priv, _, err := LoadPrivateKey(privPath)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	sig, id, err := SignBytes(priv, data)
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(SignaturePath(file), sig, 0o644); err != nil {
		return "", err
	}
	return id, nil
}

// VerifyFile verifies file against its detached .sig using the keyring.
func VerifyFile(file string, keys *Keyring) (string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	sig, err := os.ReadFile(SignaturePath(file))
	if err != nil {
		return "", err
	}
	return VerifyBytes(data, sig, keys)
}
