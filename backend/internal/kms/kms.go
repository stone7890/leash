// Package kms wraps and unwraps agent keys.
//
// It is imported by the signer and by nothing else. `cmd/leash-indexer` must not reach it in its
// import graph, and nothing under frontend/ may import a KMS client at all — those are the
// mechanisms behind invariant I1, checked over the real package graph rather than by review.
//
// Envelope encryption: a customer master key wraps a per-agent data key, and the data key encrypts
// the agent's Ed25519 seed with AES-256-GCM. KMS never sees the seed, and the database never holds
// a usable key. Compromising either alone yields nothing.
package kms

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
)

// Wrapper turns a seed into ciphertext and back. Two implementations: AWS KMS for production, and
// a local key for the sandbox demo.
type Wrapper interface {
	// Wrap encrypts a 32-byte Ed25519 seed.
	Wrap(seed []byte) (ciphertext []byte, keyRef string, err error)
	// Unwrap is the only way back to a usable key, and the only caller is the signing path.
	Unwrap(ciphertext []byte, keyRef string) (ed25519.PrivateKey, error)
}

// Key is an agent's signing key, held only inside the signer process.
type Key struct {
	priv ed25519.PrivateKey
}

func (k Key) PublicKey() ed25519.PublicKey { return k.priv.Public().(ed25519.PublicKey) }
func (k Key) Sign(message []byte) ([]byte, error) {
	if len(k.priv) == 0 {
		return nil, errors.New("kms: the key has been zeroed")
	}
	return ed25519.Sign(k.priv, message), nil
}

// Zero wipes the key material. Called on eviction from the cache and on shutdown, because a key
// resident for longer than it is needed is a key an incident can reach.
func (k *Key) Zero() {
	for i := range k.priv {
		k.priv[i] = 0
	}
	k.priv = nil
}

// Generate mints a new agent keypair.
func Generate() (seed []byte, pub ed25519.PublicKey, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return priv.Seed(), pub, nil
}

// ── The local wrapper ────────────────────────────────────────────────────────

// Local wraps with a key held on this machine.
//
// It exists so the demo runs on a laptop and on a fresh VM without an AWS account. The
// configuration REFUSES to combine it with mainnet: a locally-wrapped key protecting real money is
// exactly the shortcut that should not be one flag away from happening.
type Local struct {
	aead cipher.AEAD
}

// NewLocal derives its key from a file under the state directory, creating one on first use.
//
// The key is a file rather than an environment variable so that it does not appear in a process
// listing, a compose file, or a shell history.
func NewLocal(stateDir string) (*Local, error) {
	path := stateDir + "/local-kms.key"
	material, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		material = make([]byte, 32)
		if _, err := rand.Read(material); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, material, 0o600); err != nil {
			return nil, fmt.Errorf("writing the local key: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("reading the local key: %w", err)
	}
	if len(material) != 32 {
		return nil, fmt.Errorf("the local key at %s is %d bytes, want 32", path, len(material))
	}

	block, err := aes.NewCipher(material)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Local{aead: aead}, nil
}

func (l *Local) Wrap(seed []byte) ([]byte, string, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, "", fmt.Errorf("a seed is %d bytes, got %d", ed25519.SeedSize, len(seed))
	}
	nonce := make([]byte, l.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, "", err
	}
	// The nonce travels with the ciphertext. Reusing one with the same key would leak the
	// plaintext, so it is random per wrap and never derived from anything.
	return l.aead.Seal(nonce, nonce, seed, nil), "local", nil
}

func (l *Local) Unwrap(ciphertext []byte, keyRef string) (ed25519.PrivateKey, error) {
	if keyRef != "local" {
		return nil, fmt.Errorf(
			"this key was wrapped with %q but this signer is using the local wrapper. Refusing "+
				"rather than guessing: an agent key decrypted by the wrong path would be a "+
				"silent security failure", keyRef)
	}
	n := l.aead.NonceSize()
	if len(ciphertext) < n {
		return nil, errors.New("the wrapped key is truncated")
	}
	seed, err := l.aead.Open(nil, ciphertext[:n], ciphertext[n:], nil)
	if err != nil {
		return nil, fmt.Errorf("unwrapping the agent key: %w", err)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// APIKey mints an agent's API key and its stored hash.
//
// The key is shown ONCE. What is stored is a SHA-256 of it, so a database disclosure does not hand
// anybody a working credential — and rotation is a matter of replacing the hash, with no grace
// period, because a rotation is usually a response to a suspected leak.
func APIKey(prefix string) (key string, hash string, err error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	key = prefix + base64.RawURLEncoding.EncodeToString(raw)
	return key, HashAPIKey(key), nil
}

// HashAPIKey is how a presented key is looked up. The prefix is part of the hashed value, so a
// key cannot be moved between networks by editing it.
func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%x", sum)
}
