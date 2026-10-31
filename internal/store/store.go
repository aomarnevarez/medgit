// Package store is the content-addressed object directory and the named refs.
package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/aomarnevarez/medgit/internal/canonical"
	"github.com/aomarnevarez/medgit/internal/mederr"
)

var (
	refName = regexp.MustCompile(`^(main|episodes/[A-Za-z0-9_\-]+|pulls/[A-Za-z0-9_\-]+)$`)
	hex64   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Store reads and writes one patient record on disk.
type Store struct {
	Root string
}

// New returns a store rooted at root. The directory is created on the first write.
func New(root string) *Store {
	return &Store{Root: root}
}

// Put stores document under its canonical hash. A second put of the same bytes
// is a no-op. Different bytes at the same path are an integrity failure.
func (s *Store) Put(document map[string]any) (string, error) {
	raw, err := canonical.Bytes(document)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	path := s.objectPath(digest)
	if existing, err := os.ReadFile(path); err == nil {
		if !bytes.Equal(existing, raw) {
			return "", mederr.Integrityf("existing object %s does not match payload", digest)
		}
		return digest, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, raw, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(temporary, path); err != nil {
		return "", err
	}
	return digest, nil
}

// Get loads one object and checks that the file bytes still hash to digest.
func (s *Store) Get(digest string) (map[string]any, error) {
	if !hex64.MatchString(digest) {
		return nil, mederr.Integrityf("not a MedGit object id: %s", digest)
	}
	raw, err := os.ReadFile(s.objectPath(digest))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, mederr.Integrityf("missing object %s", digest)
		}
		return nil, err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != digest {
		return nil, mederr.Integrityf("object %s was modified on disk", digest)
	}
	decoded, err := canonical.Decode(raw)
	if err != nil {
		return nil, mederr.Integrityf("object %s is not JSON", digest)
	}
	document, ok := decoded.(map[string]any)
	if !ok {
		return nil, mederr.Integrityf("object %s is not JSON", digest)
	}
	again, err := canonical.Bytes(document)
	if err != nil || !bytes.Equal(again, raw) {
		return nil, mederr.Integrityf("object %s is not canonical", digest)
	}
	return document, nil
}

// Has reports whether an object file exists.
func (s *Store) Has(digest string) bool {
	if !hex64.MatchString(digest) {
		return false
	}
	info, err := os.Stat(s.objectPath(digest))
	return err == nil && info.Mode().IsRegular()
}

// ObjectIDs lists every stored object id.
func (s *Store) ObjectIDs() ([]string, error) {
	root := filepath.Join(s.Root, "objects")
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}
	var ids []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".tmp") {
			return nil
		}
		digest := filepath.Base(filepath.Dir(path)) + entry.Name()
		if hex64.MatchString(digest) {
			ids = append(ids, digest)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(ids)
	return ids, nil
}

// ReadRef returns the object id stored in a ref. A missing ref is ("", nil).
func (s *Store) ReadRef(name string) (string, error) {
	if err := checkRef(name); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(s.refPath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	text := strings.TrimSpace(string(raw))
	if !hex64.MatchString(text) {
		return "", mederr.Integrityf("ref %s does not point at an object id", name)
	}
	return text, nil
}

// WriteRef points name at digest. The target must already be in the store.
func (s *Store) WriteRef(name, digest string) error {
	if err := checkRef(name); err != nil {
		return err
	}
	if !hex64.MatchString(digest) {
		return mederr.Integrityf("ref target is not an object id")
	}
	if !s.Has(digest) {
		return mederr.Integrityf("ref %s would point at missing object %s", name, digest)
	}
	path := s.refPath(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, []byte(digest+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// ListRefs returns ref names, optionally filtered by prefix, in sorted order.
func (s *Store) ListRefs(prefix string) ([]string, error) {
	root := filepath.Join(s.Root, "refs")
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}
	var names []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".tmp") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			return nil
		}
		names = append(names, name)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

func (s *Store) objectPath(digest string) string {
	return filepath.Join(s.Root, "objects", digest[:2], digest[2:])
}

func (s *Store) refPath(name string) string {
	return filepath.Join(s.Root, "refs", filepath.FromSlash(name))
}

func checkRef(name string) error {
	if !refName.MatchString(name) {
		return mederr.Integrityf("illegal ref name: %s", name)
	}
	return nil
}
