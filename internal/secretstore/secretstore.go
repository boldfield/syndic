// Package secretstore persists what must be remembered about secrets: each
// secret's generation, a keyed digest of each static value, and opaque records
// (login tokens, client registrations) under composite keys.
//
// The store is one file with mode 0600 in the state directory, written
// atomically and guarded by a file lock. No function returns a secret value or
// a digest; the API returns generations and, for generic entries, the record
// the caller put there.
package secretstore

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// FileName is the store's file name inside the state directory.
const FileName = "secrets.json"

const lockSuffix = ".lock"

// ErrNotFound is returned by Get when no record exists under the key.
var ErrNotFound = errors.New("secretstore: no such record")

// Store is a handle on the secret store file. It is safe for use by several
// processes; each operation holds the file lock for its whole read-modify-write.
type Store struct {
	path string
}

type staticEntry struct {
	Generation string `json:"generation"`
	Digest     string `json:"digest"`
}

type recordEntry struct {
	Generation string `json:"generation"`
	Record     []byte `json:"record"`
}

type fileData struct {
	Key     string                 `json:"key"`
	Statics map[string]staticEntry `json:"statics"`
	Records map[string]recordEntry `json:"records"`
}

// Open returns the store in dir, which must exist. The file is created, with a
// fresh random digest key, when absent. A file whose mode allows any access to
// group or others is refused.
func Open(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, FileName)}
	err := s.update(func(*fileData) (bool, error) { return false, nil })
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Static records the current value of the static credential or env secret
// named by ref and returns its generation. The first observation, and any
// observation whose keyed digest differs from the stored one, makes a new
// random generation; an unchanged value keeps its generation.
func (s *Store) Static(ref string, value []byte) (string, error) {
	var gen string
	err := s.update(func(d *fileData) (bool, error) {
		key, err := hex.DecodeString(d.Key)
		if err != nil || len(key) == 0 {
			return false, errors.New("secretstore: corrupt digest key")
		}
		mac := hmac.New(sha256.New, key)
		mac.Write(value)
		digest := hex.EncodeToString(mac.Sum(nil))
		if e, ok := d.Statics[ref]; ok && hmac.Equal([]byte(e.Digest), []byte(digest)) {
			gen = e.Generation
			return false, nil
		}
		g, err := newGeneration()
		if err != nil {
			return false, err
		}
		d.Statics[ref] = staticEntry{Generation: g, Digest: digest}
		gen = g
		return true, nil
	})
	if err != nil {
		return "", err
	}
	return gen, nil
}

// Put stores an opaque record under the composite key. With renew, or when the
// key has no entry yet, the entry gets a new generation; otherwise it keeps its
// existing one. It returns the generation.
func (s *Store) Put(key []string, record []byte, renew bool) (string, error) {
	k := encodeKey(key)
	var gen string
	err := s.update(func(d *fileData) (bool, error) {
		if e, ok := d.Records[k]; ok && !renew {
			gen = e.Generation
		} else {
			g, err := newGeneration()
			if err != nil {
				return false, err
			}
			gen = g
		}
		d.Records[k] = recordEntry{Generation: gen, Record: append([]byte(nil), record...)}
		return true, nil
	})
	if err != nil {
		return "", err
	}
	return gen, nil
}

// Get returns the record and generation stored under the composite key, or
// ErrNotFound.
func (s *Store) Get(key []string) ([]byte, string, error) {
	k := encodeKey(key)
	var rec []byte
	var gen string
	found := false
	err := s.update(func(d *fileData) (bool, error) {
		if e, ok := d.Records[k]; ok {
			rec, gen, found = append([]byte(nil), e.Record...), e.Generation, true
		}
		return false, nil
	})
	if err != nil {
		return nil, "", err
	}
	if !found {
		return nil, "", ErrNotFound
	}
	return rec, gen, nil
}

// Delete removes the record under the composite key. Deleting an absent key is
// not an error.
func (s *Store) Delete(key []string) error {
	k := encodeKey(key)
	return s.update(func(d *fileData) (bool, error) {
		if _, ok := d.Records[k]; !ok {
			return false, nil
		}
		delete(d.Records, k)
		return true, nil
	})
}

// encodeKey joins the parts unambiguously.
func encodeKey(parts []string) string {
	b, _ := json.Marshal(parts)
	return string(b)
}

func newGeneration() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("secretstore: random generation: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// update runs fn on the file's contents under the exclusive file lock and
// writes the result atomically when fn reports a change (or the file is new).
func (s *Store) update(fn func(*fileData) (bool, error)) error {
	lock, err := os.OpenFile(s.path+lockSuffix, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("secretstore: open lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("secretstore: lock: %w", err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()

	d, isNew, err := s.read()
	if err != nil {
		return err
	}
	changed, err := fn(d)
	if err != nil {
		return err
	}
	if !changed && !isNew {
		return nil
	}
	return s.write(d)
}

func (s *Store) read() (*fileData, bool, error) {
	fi, err := os.Stat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, false, fmt.Errorf("secretstore: random key: %w", err)
		}
		return &fileData{
			Key:     hex.EncodeToString(key),
			Statics: map[string]staticEntry{},
			Records: map[string]recordEntry{},
		}, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("secretstore: stat: %w", err)
	}
	if fi.Mode().Perm()&^0600 != 0 {
		return nil, false, fmt.Errorf("secretstore: %s has mode %04o; refusing a file wider than 0600", s.path, fi.Mode().Perm())
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil, false, fmt.Errorf("secretstore: read: %w", err)
	}
	var d fileData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, false, fmt.Errorf("secretstore: parse %s: %w", s.path, err)
	}
	if d.Statics == nil {
		d.Statics = map[string]staticEntry{}
	}
	if d.Records == nil {
		d.Records = map[string]recordEntry{}
	}
	return &d, false, nil
}

func (s *Store) write(d *fileData) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("secretstore: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".secrets-*")
	if err != nil {
		return fmt.Errorf("secretstore: temp file: %w", err)
	}
	name := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("secretstore: write: %w", err)
	}
	if err := tmp.Chmod(0600); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(raw); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("secretstore: write: %w", err)
	}
	if err := os.Rename(name, s.path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("secretstore: rename: %w", err)
	}
	return nil
}
