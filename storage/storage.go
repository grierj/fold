package storage

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Store is a blob store for user-uploaded assets (avatars, group covers).
//
// The local-filesystem implementation ships now. An S3-compatible
// implementation can replace it later without changing callers: implement
// this interface (Put to the bucket, URL as a presigned or public object
// URL, Delete/Exists against the bucket) and wire it in main() based on
// config. Keys are opaque to callers — always generated server-side by
// NewKey, never taken from user input.
type Store interface {
	// Put stores the blob under key. ContentType is the validated MIME
	// type (e.g. "image/jpeg").
	Put(ctx context.Context, key string, src io.Reader, size int64, contentType string) error
	// URL returns the public URL for a stored key.
	URL(key string) string
	// Delete removes a blob. A missing key is not an error.
	Delete(ctx context.Context, key string) error
	// Exists reports whether key is stored.
	Exists(ctx context.Context, key string) (bool, error)
}

// NewKey builds a namespaced storage key: "<kind>/<owner>/<id>.<ext>".
// kind is "avatars" or "covers". The id is a random hex string so keys
// are unguessable.
func NewKey(kind, owner, id, ext string) string {
	return fmt.Sprintf("%s/%s/%s.%s", kind, owner, id, ext)
}

// ValidateKey rejects keys that could escape the store directory.
func ValidateKey(key string) error {
	if key == "" || strings.Contains(key, "..") || filepath.IsAbs(key) {
		return fmt.Errorf("invalid key")
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("invalid key")
		}
	}
	return nil
}

// LocalStore keeps blobs on the local filesystem and serves them under
// URLPrefix (e.g. "/uploads"). It is the default; see Store for the S3
// replacement seam.
type LocalStore struct {
	Dir       string // filesystem directory, e.g. /data/uploads
	URLPrefix string // URL prefix, e.g. "/uploads"
}

// NewLocalStore creates a LocalStore, creating Dir if needed.
func NewLocalStore(dir, urlPrefix string) (*LocalStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create upload dir: %w", err)
	}
	return &LocalStore{Dir: dir, URLPrefix: urlPrefix}, nil
}

func (s *LocalStore) Put(_ context.Context, key string, src io.Reader, size int64, _ string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	if size <= 0 {
		return fmt.Errorf("empty upload")
	}
	path := filepath.Join(s.Dir, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	// Write to a temp file and rename so readers never see a partial blob.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return fmt.Errorf("write blob: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close blob: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod blob: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("commit blob: %w", err)
	}
	return nil
}

func (s *LocalStore) URL(key string) string {
	segs := strings.Split(key, "/")
	escaped := make([]string, len(segs))
	for i, seg := range segs {
		escaped[i] = url.PathEscape(seg)
	}
	return strings.TrimSuffix(s.URLPrefix, "/") + "/" + strings.Join(escaped, "/")
}

func (s *LocalStore) Delete(_ context.Context, key string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	path := filepath.Join(s.Dir, filepath.FromSlash(key))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete blob: %w", err)
	}
	return nil
}

func (s *LocalStore) Exists(_ context.Context, key string) (bool, error) {
	if err := ValidateKey(key); err != nil {
		return false, err
	}
	path := filepath.Join(s.Dir, filepath.FromSlash(key))
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// LocalPath returns the filesystem path for a key. It is used by the HTTP
// handler that serves uploads; other callers should go through the Store
// interface so an S3 implementation can take over later.
func (s *LocalStore) LocalPath(key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	return filepath.Join(s.Dir, filepath.FromSlash(key)), nil
}
