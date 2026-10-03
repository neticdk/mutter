package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/zalando/go-keyring"
)

// store keeps cached messages and images on disk, encrypted with AES-GCM.
// The key lives in the OS keychain next to the login token, so a copied
// cache file, from a backup or a lost disk, can't be read without it.
type store struct {
	dir  string
	aead cipher.AEAD
}

// openStore opens the cache for user, creating its key on first use.
func openStore(user string) (*store, error) {
	key, err := storeKey()
	if err != nil {
		return nil, err
	}
	base, err := cachePath("store")
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(user))
	return newStore(filepath.Join(base, hex.EncodeToString(sum[:8])), key)
}

func newStore(dir string, key []byte) (*store, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &store{dir: dir, aead: aead}, nil
}

func storeKey() ([]byte, error) {
	s, err := keyring.Get(keyringService, "cache-key")
	if err == nil {
		return base64.StdEncoding.DecodeString(s)
	}
	if !errors.Is(err, keyring.ErrNotFound) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, keyring.Set(keyringService, "cache-key", base64.StdEncoding.EncodeToString(key))
}

// wipeStore deletes every cached file and the key, for /logout.
func wipeStore() error {
	base, err := cachePath("store")
	if err != nil {
		return err
	}
	err = keyring.Delete(keyringService, "cache-key")
	if errors.Is(err, keyring.ErrNotFound) {
		err = nil
	}
	return errors.Join(err, os.RemoveAll(base))
}

// path names the file for key in kind. Hashing the key keeps space and
// image names out of the file names.
func (s *store) path(kind, key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(s.dir, kind, hex.EncodeToString(sum[:16]))
}

// put encrypts v as JSON and writes it through a temp file. The kind and
// key are bound in as additional data, so a file can't be swapped for
// another.
func (s *store) put(kind, key string, v any) error {
	plain, err := json.Marshal(v)
	if err != nil {
		return err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	sealed := s.aead.Seal(nonce, nonce, plain, []byte(kind+"\x00"+key))
	path := s.path(kind, key)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, sealed, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// get decrypts the file for key into v. It marks the file as used, so
// prune keeps it.
func (s *store) get(kind, key string, v any) error {
	path := s.path(kind, key)
	sealed, err := os.ReadFile(path) // #nosec G304 -- path is built from a hash in the user's cache dir
	if err != nil {
		return err
	}
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return fmt.Errorf("cache file %s is truncated", path)
	}
	plain, err := s.aead.Open(nil, sealed[:n], sealed[n:], []byte(kind+"\x00"+key))
	if err != nil {
		return fmt.Errorf("cache file %s: %w", path, err)
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now) // only an LRU hint
	return json.Unmarshal(plain, v)
}

// prune deletes the least recently used files of kind until it fits in
// maxBytes.
func (s *store) prune(kind string, maxBytes int64) error {
	dir := filepath.Join(s.dir, kind)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	type file struct {
		path string
		size int64
		used time.Time
	}
	var files []file
	var total int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		files = append(files, file{filepath.Join(dir, e.Name()), info.Size(), info.ModTime()})
		total += info.Size()
	}
	slices.SortFunc(files, func(a, b file) int { return a.used.Compare(b.used) })
	for _, f := range files {
		if total <= maxBytes {
			break
		}
		if err := os.Remove(f.path); err != nil {
			return err
		}
		total -= f.size
	}
	return nil
}
