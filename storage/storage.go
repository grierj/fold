package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Key kinds. A key is "<kind>/<owner>/<name>".
const (
	KindAvatars = "avatars"
	KindCovers  = "covers"
)

// CleanBatch is the most entries one CleanOwner call looks at. Every Store
// implementation shares it, so callers can rely on one number.
const CleanBatch = 256

// Fixed-text errors. Every Store method returns one of these values itself
// (or a context error, or an error from a caller's own callback), never a
// wrapped OS error, so no error text carries a directory, a key, an owner
// id or a file name. Compare with == .
var (
	ErrInvalidKey        = errors.New("invalid storage key")
	ErrInvalidOwner      = errors.New("invalid storage owner")
	ErrUnsafeOwner       = errors.New("storage folder is not a plain folder")
	ErrUnexpectedEntry   = errors.New("storage folder holds an unexpected folder")
	ErrCleanupIncomplete = errors.New("some stored files could not be removed")
	ErrMoreRemain        = errors.New("more stored files remain")
	ErrScanFailed        = errors.New("could not list stored files")

	// Helper results. No public method returns errNoDir or errDirIO.
	errNoDir        = errors.New("folder not found")
	errDirIO        = errors.New("could not open folder")
	errNotFound     = errors.New("file not found")
	errStoreFailed  = errors.New("could not store file")
	errRemoveFailed = errors.New("could not remove file")
	errReadFailed   = errors.New("could not read file")
)

// Owner names one account's folder of avatars. Its fields are unexported, so
// code outside this package can build only the zero value (which every method
// refuses) or an owner from AvatarOwner.
type Owner struct {
	kind string
	id   uint64
}

// AvatarOwner returns the owner for a user's avatar folder. Id 0 is refused.
func AvatarOwner(id uint) (Owner, error) {
	if id == 0 {
		return Owner{}, ErrInvalidOwner
	}
	return Owner{kind: KindAvatars, id: uint64(id)}, nil
}

// OwnerEntries is what ScanOwners reports for one owner folder. Keys lists
// at most the scan's maxNames entries; Truncated says more exist. Unsafe
// means the folder is a symlink, not a folder, or could not be opened or
// read; it carries no keys.
type OwnerEntries struct {
	Owner     uint64
	Keys      []string
	Truncated bool
	Unsafe    bool
}

// ScanStats counts what ScanOwners saw: Owners is how many entries it passed
// to the callback, SkippedNames how many names under the kind folder were
// not canonical decimal ids. It never holds the names themselves.
type ScanStats struct {
	Owners       int
	SkippedNames int
}

// Store is a blob store for user-uploaded assets (avatars, group covers).
//
// The local-filesystem implementation ships now. An S3-compatible
// implementation can replace it later without changing callers: implement
// this interface (Put to the bucket, URL as a presigned or public object
// URL, Delete against the bucket) and wire it in main() based on
// config. Keys are opaque to callers — always generated server-side by
// NewKey, never taken from user input.
//
// Contract for every implementation:
//   - Errors are fixed text. No error contains a key, a prefix, a bucket
//     path, an owner id or a file name.
//   - CleanOwner and ScanOwners are the only calls that list. An S3
//     implementation must list by the exact "<kind>/<id>/" prefix, trailing
//     slash included, so owner 1 never matches owner 10, must page deletes
//     with the keep key excluded at most CleanBatch per call, and must never
//     put a key, prefix or bucket path in an error. DEC-140 cut S3, so none
//     is built; LocalStore is the only implementer.
//
// A store may additionally implement Open(string) (*os.File, error) for
// local serving. It must open only the exact regular-file entry named by the
// key, refuse symlinks and avoid blocking on non-regular entries. serveUpload
// streams that file; stores without Open redirect to URL(key).
type Store interface {
	// Put stores the blob under key. ContentType is the validated MIME
	// type (e.g. "image/jpeg").
	Put(ctx context.Context, key string, src io.Reader, size int64, contentType string) error
	// URL returns the public URL for a stored key.
	URL(key string) string
	// Delete removes a blob. A missing key is not an error.
	Delete(ctx context.Context, key string) error
	// CleanOwner removes the avatar owner's objects except the exact keep
	// key, at most CleanBatch per call, and returns ErrMoreRemain when more
	// may remain. It rejects any owner that is not an avatar owner with
	// ErrInvalidOwner. A keep that does not parse, or parses to another kind
	// or owner, keeps nothing and never touches another namespace. A missing
	// namespace is success. When ctx ends it stops before the next removal
	// and returns ctx.Err(); removals already done stay done. Choosing the
	// keep key is the caller's job: the store keeps exactly the key it is
	// given and knows nothing about account status.
	CleanOwner(ctx context.Context, owner Owner, keep string) error
	// ScanOwners streams the owners of one kind (only KindAvatars) once per
	// canonical numeric owner name, and lists at most maxNames (1 to 1000)
	// entries for each. A non-nil error from fn stops the scan and is
	// returned unchanged. The stats count everything seen so far, even when
	// an error comes back.
	ScanOwners(ctx context.Context, kind string, maxNames int, fn func(OwnerEntries) error) (ScanStats, error)
}

// NewKey builds a namespaced storage key: "<kind>/<owner>/<id>.<ext>".
// kind is KindAvatars or KindCovers. The id is a random hex string so keys
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

// parseOwnerName accepts a canonical decimal id: digits only, no sign, no
// leading zero, greater than zero, fits in uint64.
func parseOwnerName(s string) (uint64, bool) {
	if s == "" || s[0] == '0' {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseUint(s, 10, 64)
	if err != nil || id == 0 {
		return 0, false
	}
	return id, true
}

// ParseKey splits a key into its kind, owner and file name. It accepts
// exactly three segments: a known kind, a canonical decimal owner, and a
// name that is at most 255 bytes, does not start with '.' and holds no '/'
// or '\'. Every LocalStore method that touches a blob goes through it.
func ParseKey(key string) (kind string, owner uint64, name string, err error) {
	if ValidateKey(key) != nil {
		return "", 0, "", ErrInvalidKey
	}
	parts := strings.Split(key, "/")
	if len(parts) != 3 {
		return "", 0, "", ErrInvalidKey
	}
	if parts[0] != KindAvatars && parts[0] != KindCovers {
		return "", 0, "", ErrInvalidKey
	}
	id, ok := parseOwnerName(parts[1])
	if !ok {
		return "", 0, "", ErrInvalidKey
	}
	n := parts[2]
	if n == "" || len(n) > 255 || n[0] == '.' || strings.ContainsAny(n, "/\\") {
		return "", 0, "", ErrInvalidKey
	}
	return parts[0], id, n, nil
}

// LocalStore keeps blobs on the local filesystem and serves them under
// URLPrefix (e.g. "/uploads"). It is the default; see Store for the S3
// replacement seam.
//
// Dir (UPLOAD_DIR) is trusted operator configuration and may itself be a
// mount or a symlink. Every operation opens it as an os.Root and works only
// through root-relative handles, so containment starts below Dir. os.Root
// follows symlinks that stay inside the root, so every folder and file this
// store acts on is bound by an Lstat, then an open, then an os.SameFile check
// of the opened handle against what Lstat saw. Hard links, bind mounts and
// filesystem calls that never return are not defended.
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

// lstatDir returns the FileInfo of the real directory called name under
// parent. With create set it first makes the directory and accepts one that
// already exists; only the Lstat after the Mkdir is trusted. A missing name
// is errNoDir, a symlink or non-directory is ErrUnsafeOwner and any other
// failure is errDirIO.
func lstatDir(parent *os.Root, name string, create bool) (fs.FileInfo, error) {
	if create {
		if err := parent.Mkdir(name, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, errDirIO
		}
	}
	fi, err := parent.Lstat(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, errNoDir
		}
		return nil, errDirIO
	}
	if !fi.IsDir() {
		return nil, ErrUnsafeOwner
	}
	return fi, nil
}

// openVerifiedDir opens name under parent and requires the opened handle to
// be the very directory lstatDir saw. A mismatch (a swap after the Lstat)
// is ErrUnsafeOwner; an open or stat failure is errDirIO. The handle is
// closed on every failure.
func openVerifiedDir(parent *os.Root, name string, want fs.FileInfo) (*os.Root, error) {
	// Make name an intermediate component, which must be opened as a directory.
	// Opening a FIFO as the terminal component could otherwise block forever.
	child, err := parent.OpenRoot(name + "/.")
	if err != nil {
		return nil, errDirIO
	}
	got, err := child.Stat(".")
	if err != nil {
		child.Close()
		return nil, errDirIO
	}
	if !os.SameFile(want, got) {
		child.Close()
		return nil, ErrUnsafeOwner
	}
	return child, nil
}

// openOwnerDir binds the kind folder, then the decimal owner folder, each
// made (when create is set), Lstat'd and opened through its already-verified
// parent. It returns both handles; the caller closes both. A helper sentinel
// from either level is passed up unchanged.
func openOwnerDir(root *os.Root, kind string, id uint64, create bool) (kindRoot, ownerRoot *os.Root, err error) {
	kfi, err := lstatDir(root, kind, create)
	if err != nil {
		return nil, nil, err
	}
	kindRoot, err = openVerifiedDir(root, kind, kfi)
	if err != nil {
		return nil, nil, err
	}
	idName := strconv.FormatUint(id, 10)
	ofi, err := lstatDir(kindRoot, idName, create)
	if err != nil {
		kindRoot.Close()
		return nil, nil, err
	}
	ownerRoot, err = openVerifiedDir(kindRoot, idName, ofi)
	if err != nil {
		kindRoot.Close()
		return nil, nil, err
	}
	return kindRoot, ownerRoot, nil
}

// lstatEntry returns the FileInfo of a regular file called name in the owner
// folder. A missing or non-regular entry (symlink, directory, FIFO, device)
// is errNotFound; any other failure is errReadFailed.
func lstatEntry(ownerRoot *os.Root, name string) (fs.FileInfo, error) {
	fi, err := ownerRoot.Lstat(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, errNotFound
		}
		return nil, errReadFailed
	}
	if !fi.Mode().IsRegular() {
		return nil, errNotFound
	}
	return fi, nil
}

// openVerifiedEntry opens the file lstatEntry saw and requires the opened
// file to be a regular file with the same identity. O_NONBLOCK keeps a FIFO
// swapped in after the Lstat from blocking the caller.
func openVerifiedEntry(ownerRoot *os.Root, name string, want fs.FileInfo) (*os.File, error) {
	f, err := ownerRoot.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, errNotFound
		}
		return nil, errReadFailed
	}
	got, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, errReadFailed
	}
	if !got.Mode().IsRegular() || !os.SameFile(want, got) {
		f.Close()
		return nil, errNotFound
	}
	return f, nil
}

// readDirUpTo reads up to limit entries from d and says whether more exist.
// os.File.ReadDir(n) promises at most n entries per call, so a single call
// cannot tell "exactly n" from "more"; this loops until it holds limit+1
// entries or gets io.EOF. Entries returned together with io.EOF are kept. A
// call that returns nothing and no error ends the loop so a broken reader
// cannot spin. On any other error it returns no entries and that error, so
// no caller can act on a partial read. Memory stays at limit+1 entries.
func readDirUpTo(d interface {
	ReadDir(int) ([]fs.DirEntry, error)
}, limit int) (entries []fs.DirEntry, more bool, err error) {
	for len(entries) < limit+1 {
		want := limit + 1 - len(entries)
		batch, rerr := d.ReadDir(want)
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return nil, false, rerr
		}
		if len(batch) > want {
			batch = batch[:want]
		}
		entries = append(entries, batch...)
		if rerr != nil || len(batch) == 0 {
			break
		}
	}
	if len(entries) > limit {
		return entries[:limit], true, nil
	}
	return entries, false, nil
}

// ctxReader fails reads once ctx has ended, so a cancelled Put stops copying.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// Put writes the blob to a temporary file in the owner folder and renames it
// into place, so readers never see a partial blob.
func (s *LocalStore) Put(ctx context.Context, key string, src io.Reader, size int64, _ string) error {
	kind, owner, name, err := ParseKey(key)
	if err != nil {
		return ErrInvalidKey
	}
	if size <= 0 {
		return errStoreFailed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return errStoreFailed
	}
	defer root.Close()
	kindRoot, ownerRoot, err := openOwnerDir(root, kind, owner, true)
	if err != nil {
		if err == ErrUnsafeOwner {
			return ErrUnsafeOwner
		}
		return errStoreFailed
	}
	defer kindRoot.Close()
	defer ownerRoot.Close()

	var rnd [16]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return errStoreFailed
	}
	tmp := ".tmp-" + hex.EncodeToString(rnd[:])
	// O_EXCL never follows a symlink, so the temp file is new.
	f, err := ownerRoot.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return errStoreFailed
	}
	fail := func(result error) error {
		f.Close()
		_ = ownerRoot.Remove(tmp)
		return result
	}
	if err := f.Chmod(0o644); err != nil {
		return fail(errStoreFailed)
	}
	if _, err := io.Copy(f, ctxReader{ctx: ctx, r: src}); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return fail(cerr)
		}
		return fail(errStoreFailed)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = ownerRoot.Remove(tmp)
		return errStoreFailed
	}
	if err := ctx.Err(); err != nil {
		_ = ownerRoot.Remove(tmp)
		return err
	}
	// Rename is the last step: it replaces the directory entry without
	// following it, and Put succeeds only after it does.
	if err := ownerRoot.Rename(tmp, name); err != nil {
		_ = ownerRoot.Remove(tmp)
		return errStoreFailed
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

// Delete unlinks the entry itself (never a symlink's target). A missing key
// or folder is not an error.
func (s *LocalStore) Delete(_ context.Context, key string) error {
	kind, owner, name, err := ParseKey(key)
	if err != nil {
		return ErrInvalidKey
	}
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return errRemoveFailed
	}
	defer root.Close()
	kindRoot, ownerRoot, err := openOwnerDir(root, kind, owner, false)
	switch err {
	case nil:
	case errNoDir:
		return nil
	case ErrUnsafeOwner:
		return ErrUnsafeOwner
	default:
		return errRemoveFailed
	}
	defer kindRoot.Close()
	defer ownerRoot.Close()
	if err := ownerRoot.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errRemoveFailed
	}
	return nil
}

// Open returns the stored file for key. It returns only a regular file that
// is the exact entry named, never a symlink's target, and never blocks on a
// FIFO. Every failure is a fixed-text error. The caller closes the file.
func (s *LocalStore) Open(key string) (*os.File, error) {
	kind, owner, name, err := ParseKey(key)
	if err != nil {
		return nil, ErrInvalidKey
	}
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return nil, errReadFailed
	}
	defer root.Close()
	kindRoot, ownerRoot, err := openOwnerDir(root, kind, owner, false)
	switch err {
	case nil:
	case errNoDir:
		return nil, errNotFound
	case ErrUnsafeOwner:
		return nil, ErrUnsafeOwner
	default:
		return nil, errReadFailed
	}
	defer kindRoot.Close()
	defer ownerRoot.Close()
	fi, err := lstatEntry(ownerRoot, name)
	if err != nil {
		return nil, err
	}
	return openVerifiedEntry(ownerRoot, name, fi)
}

// CleanOwner removes the avatar owner's files except the exact keep key, at
// most CleanBatch entries per call. See Store.CleanOwner for the contract.
//
// Nothing is recursive: a real directory inside the owner folder is left
// untouched and reported as ErrUnexpectedEntry; anything else (files, temp
// remnants, symlinks, FIFOs) is unlinked without following it.
func (s *LocalStore) CleanOwner(ctx context.Context, owner Owner, keep string) error {
	if owner.kind != KindAvatars || owner.id == 0 {
		return ErrInvalidOwner
	}
	keepName := ""
	if keep != "" {
		if k, o, n, err := ParseKey(keep); err == nil && k == KindAvatars && o == owner.id {
			keepName = n
		}
	}
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return ErrCleanupIncomplete
	}
	defer root.Close()
	kindRoot, ownerRoot, err := openOwnerDir(root, owner.kind, owner.id, false)
	switch err {
	case nil:
	case errNoDir:
		return nil
	case ErrUnsafeOwner:
		return ErrUnsafeOwner
	default:
		return ErrCleanupIncomplete
	}
	defer kindRoot.Close()
	defer ownerRoot.Close()

	dir, err := ownerRoot.Open(".")
	if err != nil {
		return ErrCleanupIncomplete
	}
	entries, more, err := readDirUpTo(dir, CleanBatch)
	dir.Close()
	if err != nil {
		return ErrCleanupIncomplete
	}

	var sawNested, failed bool
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := e.Name()
		if keepName != "" && name == keepName {
			continue
		}
		// Never trust the DirEntry type bits: they can be unknown.
		fi, err := ownerRoot.Lstat(name)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				failed = true
			}
			continue
		}
		if fi.IsDir() {
			sawNested = true
			continue
		}
		if err := ownerRoot.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			failed = true
		}
	}
	switch {
	case sawNested:
		return ErrUnexpectedEntry
	case failed:
		return ErrCleanupIncomplete
	case more:
		return ErrMoreRemain
	case keepName != "":
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return removeOwnerDir(kindRoot, ownerRoot, owner.id)
}

// removeOwnerDir removes the now-empty owner folder. Remove on a Root can
// delete only an empty directory or one entry, so it never reaches contents.
func removeOwnerDir(kindRoot, ownerRoot *os.Root, id uint64) error {
	name := strconv.FormatUint(id, 10)
	fi, err := kindRoot.Lstat(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return ErrCleanupIncomplete
	}
	if !fi.IsDir() {
		return nil
	}
	got, err := ownerRoot.Stat(".")
	if err != nil {
		return ErrCleanupIncomplete
	}
	if !os.SameFile(fi, got) {
		return nil
	}
	err = kindRoot.Remove(name)
	if err == nil || errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
		return nil
	}
	return ErrCleanupIncomplete
}

// ScanOwners streams the owner folders of one kind. See Store.ScanOwners.
func (s *LocalStore) ScanOwners(ctx context.Context, kind string, maxNames int, fn func(OwnerEntries) error) (ScanStats, error) {
	var stats ScanStats
	if kind != KindAvatars || maxNames < 1 || maxNames > 1000 {
		return stats, ErrInvalidOwner
	}
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return stats, ErrScanFailed
	}
	defer root.Close()
	kfi, err := lstatDir(root, kind, false)
	switch err {
	case nil:
	case errNoDir:
		return stats, nil
	case ErrUnsafeOwner:
		return stats, ErrUnsafeOwner
	default:
		return stats, ErrScanFailed
	}
	kindRoot, err := openVerifiedDir(root, kind, kfi)
	switch err {
	case nil:
	case ErrUnsafeOwner:
		return stats, ErrUnsafeOwner
	default:
		return stats, ErrScanFailed
	}
	defer kindRoot.Close()
	dir, err := kindRoot.Open(".")
	if err != nil {
		return stats, ErrScanFailed
	}
	defer dir.Close()

	emit := func(oe OwnerEntries) error {
		stats.Owners++
		return fn(oe)
	}
	for {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		batch, rerr := dir.ReadDir(256)
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return stats, ErrScanFailed
		}
		for _, e := range batch {
			if err := ctx.Err(); err != nil {
				return stats, err
			}
			idName := e.Name()
			id, ok := parseOwnerName(idName)
			if !ok {
				stats.SkippedNames++
				continue
			}
			oe, ok := scanOwner(kindRoot, kind, id, idName, maxNames)
			if !ok {
				continue
			}
			if err := emit(oe); err != nil {
				return stats, err
			}
		}
		if rerr != nil || len(batch) == 0 {
			return stats, nil
		}
	}
}

// scanOwner lists one owner folder. It reports false for a folder that
// vanished. A symlink, a non-folder or a folder it cannot open or read is
// reported Unsafe with no keys, so one bad folder never aborts the scan.
func scanOwner(kindRoot *os.Root, kind string, id uint64, idName string, maxNames int) (OwnerEntries, bool) {
	unsafe := OwnerEntries{Owner: id, Unsafe: true}
	fi, err := lstatDir(kindRoot, idName, false)
	if err == errNoDir {
		return OwnerEntries{}, false
	}
	if err != nil {
		return unsafe, true
	}
	ownerRoot, err := openVerifiedDir(kindRoot, idName, fi)
	if err != nil {
		return unsafe, true
	}
	defer ownerRoot.Close()
	dir, err := ownerRoot.Open(".")
	if err != nil {
		return unsafe, true
	}
	defer dir.Close()
	entries, more, err := readDirUpTo(dir, maxNames)
	if err != nil {
		return unsafe, true
	}
	keys := make([]string, 0, len(entries))
	for _, e := range entries {
		keys = append(keys, kind+"/"+idName+"/"+e.Name())
	}
	return OwnerEntries{Owner: id, Keys: keys, Truncated: more}, true
}
