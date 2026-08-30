package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ataidesorg/ink/internal/config"
)

const (
	storeFileName  = "secrets.enc"
	keyFileName    = "secrets.key"
	storeVersion   = 0x01
	nonceLen       = 12
	keyringService = "ink"
	keyringKeyName = "secret-store-key"
)

// resolveStore reads one name from the encrypted secret store.
func (r *Resolver) resolveStore(name string) (*Credential, error) {
	v, found, err := r.storeGet(name)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &ErrNoCredential{Source: "secret_store", Where: name}
	}
	return r.credential(v), nil
}

// storeKey picks the store entry name from an AuthRef: name, then
// account, then id (the shape `{ source = "secret_store", id = "x" }`
// that `ink auth set x` stores under).
func storeKey(ref config.AuthRef) string {
	if ref.Name != "" {
		return ref.Name
	}
	if ref.Account != "" {
		return ref.Account
	}
	return ref.ID
}

// storeGet reads the store; a missing file is a clean miss, a corrupt file
// is an error (fail closed, never silently empty).
func (r *Resolver) storeGet(name string) (string, bool, error) {
	path, err := config.StateFilePath(r.getenv, storeFileName)
	if err != nil {
		return "", false, err
	}
	raw, err := os.ReadFile(path) //nolint:gosec // path minted by StateFilePath under the state dir
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read secret store %s: %w", path, err)
	}
	key, err := r.storeEncryptionKey(false)
	if err != nil {
		return "", false, err
	}
	m, err := decryptStore(raw, key)
	if err != nil {
		return "", false, fmt.Errorf("secret store %s: %w", path, err)
	}
	v, ok := m[name]
	return v, ok, nil
}

// StoreSet writes name into the encrypted secret store (atomic, 0600).
// `ink auth set` is the intended caller; the value must arrive via
// prompt or stdin, never argv.
func (r *Resolver) StoreSet(name, value string) error {
	path, err := config.StateFilePath(r.getenv, storeFileName)
	if err != nil {
		return err
	}
	key, err := r.storeEncryptionKey(true)
	if err != nil {
		return err
	}
	m := map[string]string{}
	if raw, err := os.ReadFile(path); err == nil { //nolint:gosec // path minted by StateFilePath under the state dir
		if m, err = decryptStore(raw, key); err != nil {
			return fmt.Errorf("secret store %s: %w", path, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read secret store %s: %w", path, err)
	}
	next := make(map[string]string, len(m)+1)
	for k, v := range m {
		next[k] = v
	}
	next[name] = value
	r.register.AddLiteral(value)
	raw, err := encryptStore(next, key)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, raw)
}

// StoreDelete removes name from the encrypted secret store. A missing
// store or entry is a clean false, not an error.
func (r *Resolver) StoreDelete(name string) (bool, error) {
	path, err := config.StateFilePath(r.getenv, storeFileName)
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(path) //nolint:gosec // path minted by StateFilePath under the state dir
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read secret store %s: %w", path, err)
	}
	key, err := r.storeEncryptionKey(false)
	if err != nil {
		return false, err
	}
	m, err := decryptStore(raw, key)
	if err != nil {
		return false, fmt.Errorf("secret store %s: %w", path, err)
	}
	if _, ok := m[name]; !ok {
		return false, nil
	}
	next := make(map[string]string, len(m)-1)
	for k, v := range m {
		if k != name {
			next[k] = v
		}
	}
	out, err := encryptStore(next, key)
	if err != nil {
		return false, err
	}
	return true, writeFileAtomic(path, out)
}

// storeEncryptionKey loads the AES-256 key: keyring first, then the key
// file beside the store. create=true generates and persists a missing key —
// in the keyring when one exists, else in a 0600 file with a warning.
func (r *Resolver) storeEncryptionKey(create bool) ([]byte, error) {
	ctx := context.Background()
	v, found, lookErr := r.keyring(ctx, keyringService, keyringKeyName)
	if lookErr == nil && found {
		return decodeKey(v)
	}
	keyPath, err := config.StateFilePath(r.getenv, keyFileName)
	if err != nil {
		return nil, err
	}
	if raw, err := os.ReadFile(keyPath); err == nil { //nolint:gosec // path minted by StateFilePath under the state dir
		return decodeKey(trimEOL(string(raw)))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read secret store key %s: %w", keyPath, err)
	}
	if !create {
		return nil, fmt.Errorf("secret store key missing (looked in keyring %s/%s and %s); run `ink auth set <provider>` to create the store", keyringService, keyringKeyName, keyPath)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate secret store key: %w", err)
	}
	encoded := hex.EncodeToString(key)
	// A lookup that reported the keyring unavailable rules out storing there
	// too; anything else earns one attempt before the file fallback.
	if !errors.Is(lookErr, ErrKeyringUnavailable) {
		if err := r.keyringStore(ctx, keyringService, keyringKeyName, encoded); err == nil {
			return key, nil
		}
	}
	r.warnf("no OS keyring for the secret-store key; keeping it in %s (0600). Anyone with that file can read the store.", keyPath)
	if err := writeFileAtomic(keyPath, []byte(encoded)); err != nil {
		return nil, err
	}
	return key, nil
}

func decodeKey(s string) ([]byte, error) {
	key, err := hex.DecodeString(s)
	if err != nil || len(key) != 32 {
		return nil, errors.New("secret store key is corrupt (expected 64 hex characters)")
	}
	return key, nil
}

func sealer(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secret store cipher: %w", err)
	}
	return cipher.NewGCM(block)
}

func encryptStore(m map[string]string, key []byte) ([]byte, error) {
	plain, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode secret store: %w", err)
	}
	aead, err := sealer(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secret store nonce: %w", err)
	}
	out := make([]byte, 0, 1+nonceLen+len(plain)+aead.Overhead())
	out = append(out, storeVersion)
	out = append(out, nonce...)
	return aead.Seal(out, nonce, plain, nil), nil
}

func decryptStore(raw, key []byte) (map[string]string, error) {
	if len(raw) < 1+nonceLen || raw[0] != storeVersion {
		return nil, errors.New("corrupt or unsupported store format")
	}
	aead, err := sealer(key)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, raw[1:1+nonceLen], raw[1+nonceLen:], nil)
	if err != nil {
		return nil, errors.New("decrypt failed (wrong key or corrupt file)")
	}
	var m map[string]string
	if err := json.Unmarshal(plain, &m); err != nil {
		return nil, fmt.Errorf("decode secret store: %w", err)
	}
	return m, nil
}

// writeFileAtomic writes 0600 via temp file + rename in the same directory.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-secrets-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
