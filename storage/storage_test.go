package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func testStore(t *testing.T) *LocalStore {
	t.Helper()
	s, err := NewLocalStore(t.TempDir(), "/uploads")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s
}

// put writes a file under the store's directory, making parents.
func put(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func symlink(t *testing.T, dir, target, rel string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatalf("symlink %s: %v", rel, err)
	}
}

func exists(dir, rel string) bool {
	_, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel)))
	return err == nil
}

// snapshot records every entry under root without following symlinks, so a
// test can prove a tree is byte-for-byte unchanged.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			out[rel] = "link:" + target
		case fi.IsDir():
			out[rel] = "dir"
		case fi.Mode().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[rel] = "file:" + string(b)
		default:
			out[rel] = "other:" + fi.Mode().String()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return out
}

func sameTree(t *testing.T, what string, before, after map[string]string) {
	t.Helper()
	if reflect.DeepEqual(before, after) {
		return
	}
	var diffs []string
	for k, v := range before {
		if got, ok := after[k]; !ok {
			diffs = append(diffs, "removed "+k)
		} else if got != v {
			diffs = append(diffs, "changed "+k)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			diffs = append(diffs, "added "+k)
		}
	}
	sort.Strings(diffs)
	t.Fatalf("%s changed: %v", what, diffs)
}

func avatarOwner(t *testing.T, id uint) Owner {
	t.Helper()
	o, err := AvatarOwner(id)
	if err != nil {
		t.Fatalf("avatar owner %d: %v", id, err)
	}
	return o
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
}

// chmod changes a mode and restores 0755 in t.Cleanup (which runs before
// t.TempDir's removal), so the temp tree can always be deleted.
func chmod(t *testing.T, p string, mode os.FileMode) {
	t.Helper()
	t.Cleanup(func() { _ = os.Chmod(p, 0o755) })
	if err := os.Chmod(p, mode); err != nil {
		t.Fatalf("chmod %s: %v", p, err)
	}
}

// noLeak fails when an error's text could carry a path, key, name or id.
func noLeak(t *testing.T, err error, dir string) {
	t.Helper()
	if err == nil {
		return
	}
	text := err.Error()
	for _, bad := range []string{dir, "avatars", "covers", ".tmp-", "/"} {
		if strings.Contains(text, bad) {
			t.Fatalf("error %q leaks %q", text, bad)
		}
	}
	if strings.ContainsAny(text, "0123456789") {
		t.Fatalf("error %q holds a digit (an owner id or name?)", text)
	}
}

func TestPutExistsDelete(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	key := NewKey("avatars", "42", "abc123", "png")
	data := []byte("fake-png-bytes")

	if err := s.Put(ctx, key, bytes.NewReader(data), int64(len(data)), "image/png"); err != nil {
		t.Fatalf("put: %v", err)
	}
	ok, err := s.Exists(ctx, key)
	if err != nil || !ok {
		t.Fatalf("exists after put: %v %v", ok, err)
	}
	if u := s.URL(key); u != "/uploads/avatars/42/abc123.png" {
		t.Fatalf("url %q", u)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	ok, err = s.Exists(ctx, key)
	if err != nil || ok {
		t.Fatalf("exists after delete: %v %v", ok, err)
	}
	// Deleting a missing key is not an error.
	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
}

func TestPutRejectsTraversal(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, key := range []string{"../evil", "a/../../b", "/abs", "", "a//b"} {
		if err := s.Put(ctx, key, strings.NewReader("x"), 1, "image/png"); err == nil {
			t.Fatalf("put accepted bad key %q", key)
		}
		if _, err := s.Exists(ctx, key); err == nil {
			t.Fatalf("exists accepted bad key %q", key)
		}
	}
}

func TestPutRejectsEmpty(t *testing.T) {
	s := testStore(t)
	err := s.Put(context.Background(), "avatars/1/b.png", strings.NewReader(""), 0, "image/png")
	if err != errStoreFailed {
		t.Fatalf("put of an empty upload = %v, want errStoreFailed", err)
	}
	if exists(s.Dir, "avatars") {
		t.Fatalf("an empty upload made a folder")
	}
}

func TestURLEscapesSegments(t *testing.T) {
	s := testStore(t)
	// Keys are server-generated, but URL escaping must still be safe.
	if u := s.URL("avatars/1/a b.png"); !strings.Contains(u, "a%20b.png") {
		t.Fatalf("url not escaped: %q", u)
	}
}

func TestParseKey(t *testing.T) {
	kind, owner, name, err := ParseKey("avatars/7/abc.png")
	if err != nil || kind != KindAvatars || owner != 7 || name != "abc.png" {
		t.Fatalf("avatars key: %q %d %q %v", kind, owner, name, err)
	}
	kind, owner, name, err = ParseKey("covers/18446744073709551615/x.jpg")
	if err != nil || kind != KindCovers || owner != 18446744073709551615 || name != "x.jpg" {
		t.Fatalf("covers key: %q %d %q %v", kind, owner, name, err)
	}
	if _, _, _, err := ParseKey("avatars/1/" + strings.Repeat("a", 255)); err != nil {
		t.Fatalf("255-byte name refused: %v", err)
	}
	bad := []string{
		"", "avatars", "avatars/1", "avatars/1/a/b", "a/b/c/d",
		"avatars//a.png", "avatars/1/",
		"avatars/01/a.png", "avatars/0/a.png", "avatars/+1/a.png", "avatars/-1/a.png",
		"avatars/1a/a.png", "avatars/18446744073709551616/a.png",
		"avatars/1/.hidden", "avatars/1/.tmp-abc", "avatars/1/..", "avatars/1/a..b",
		"avatars/1/a\\b.png", "other/1/a.png", "Avatars/1/a.png", "/avatars/1/a.png",
		"avatars/1/" + strings.Repeat("a", 256),
	}
	for _, key := range bad {
		if _, _, _, err := ParseKey(key); err != ErrInvalidKey {
			t.Fatalf("ParseKey(%q) = %v, want ErrInvalidKey", key, err)
		}
	}
}

func TestStoreMethodsRefuseKeysParseKeyRefuses(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, key := range []string{"avatars/1", "avatars/01/a.png", "avatars/1/.hidden", "other/1/a.png", "avatars/1/a/b"} {
		if err := s.Put(ctx, key, strings.NewReader("x"), 1, "image/png"); err != ErrInvalidKey {
			t.Fatalf("Put(%q) = %v", key, err)
		}
		if err := s.Delete(ctx, key); err != ErrInvalidKey {
			t.Fatalf("Delete(%q) = %v", key, err)
		}
		if ok, err := s.Exists(ctx, key); ok || err != ErrInvalidKey {
			t.Fatalf("Exists(%q) = %v %v", key, ok, err)
		}
		if f, err := s.Open(key); f != nil || err != ErrInvalidKey {
			t.Fatalf("Open(%q) = %v %v", key, f, err)
		}
	}
	if exists(s.Dir, "avatars") {
		t.Fatalf("a refused key made a folder")
	}
}

func TestPutIsAtomicAndMode0644(t *testing.T) {
	s := testStore(t)
	key := "avatars/1/new.png"
	var sawTmp, sawTarget bool
	src := &probeReader{chunks: []string{"abc", "def"}, between: func() {
		entries, err := os.ReadDir(filepath.Join(s.Dir, "avatars", "1"))
		if err != nil {
			t.Errorf("read owner dir: %v", err)
			return
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".tmp-") {
				sawTmp = true
			}
			if e.Name() == "new.png" {
				sawTarget = true
			}
		}
	}}
	if err := s.Put(context.Background(), key, src, 6, "image/png"); err != nil {
		t.Fatalf("put: %v", err)
	}
	if !sawTmp || sawTarget {
		t.Fatalf("mid-copy: tmp %v, target visible %v", sawTmp, sawTarget)
	}
	got, err := os.ReadFile(filepath.Join(s.Dir, "avatars", "1", "new.png"))
	if err != nil || string(got) != "abcdef" {
		t.Fatalf("content %q %v", got, err)
	}
	fi, err := os.Stat(filepath.Join(s.Dir, "avatars", "1", "new.png"))
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v %v", fi.Mode(), err)
	}
	entries, _ := os.ReadDir(filepath.Join(s.Dir, "avatars", "1"))
	if len(entries) != 1 {
		t.Fatalf("leftover entries: %d", len(entries))
	}
}

// probeReader returns its chunks one per Read and calls between after the
// first, so a test can look at the folder in the middle of a copy.
type probeReader struct {
	chunks  []string
	n       int
	between func()
}

type cancelAtEOF struct{ cancel context.CancelFunc }

func (r cancelAtEOF) Read(p []byte) (int, error) {
	r.cancel()
	return copy(p, "final"), io.EOF
}

func TestPutCancelledOnFinalReadLeavesNothing(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Put(ctx, "avatars/1/new.png", cancelAtEOF{cancel}, 5, "image/png"); err != context.Canceled {
		t.Fatalf("put = %v, want context.Canceled", err)
	}
	entries, err := os.ReadDir(filepath.Join(s.Dir, "avatars", "1"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancelled put left entries %v: %v", entries, err)
	}
}

func TestDirectorySwappedForFIFOIsRejectedPromptly(t *testing.T) {
	s := testStore(t)
	put(t, s.Dir, "avatars/1/a.png", "one")
	root, err := os.OpenRoot(filepath.Join(s.Dir, "avatars"))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	fi, err := lstatDir(root, "1", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(s.Dir, "avatars/1"), filepath.Join(s.Dir, "avatars/moved")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(s.Dir, "avatars/1"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		h, err := openVerifiedDir(root, "1", fi)
		if h != nil {
			h.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("opened FIFO as a directory")
		}
	case <-time.After(time.Second):
		t.Fatal("opening FIFO blocked")
	}
}

func (p *probeReader) Read(b []byte) (int, error) {
	if p.n >= len(p.chunks) {
		return 0, io.EOF
	}
	if p.n == 1 && p.between != nil {
		p.between()
	}
	c := p.chunks[p.n]
	p.n++
	return copy(b, c), nil
}

func TestPutCancelledLeavesNothing(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	src := &probeReader{chunks: []string{"abc", "def"}, between: cancel}
	err := s.Put(ctx, "avatars/1/new.png", src, 6, "image/png")
	if err != context.Canceled {
		t.Fatalf("put with a cancelled ctx = %v, want context.Canceled", err)
	}
	noLeak(t, err, s.Dir)
	entries, _ := os.ReadDir(filepath.Join(s.Dir, "avatars", "1"))
	if len(entries) != 0 {
		t.Fatalf("a cancelled put left %d entries", len(entries))
	}
	// A context that ended before the call writes nothing at all.
	s2 := testStore(t)
	if err := s2.Put(ctx, "avatars/1/new.png", strings.NewReader("x"), 1, "image/png"); err != context.Canceled {
		t.Fatalf("put with a dead ctx = %v", err)
	}
	if exists(s2.Dir, "avatars") {
		t.Fatalf("a dead ctx made a folder")
	}
}

func TestPutReplacesExistingKey(t *testing.T) {
	s := testStore(t)
	put(t, s.Dir, "avatars/1/a.png", "old")
	if err := s.Put(context.Background(), "avatars/1/a.png", strings.NewReader("new!"), 4, "image/png"); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(s.Dir, "avatars", "1", "a.png"))
	if string(got) != "new!" {
		t.Fatalf("content %q", got)
	}
}

func TestOwnerContract(t *testing.T) {
	if _, err := AvatarOwner(0); err != ErrInvalidOwner {
		t.Fatalf("AvatarOwner(0) = %v", err)
	}
	s := testStore(t)
	put(t, s.Dir, "avatars/1/a.png", "one")
	put(t, s.Dir, "covers/1/a.png", "cover")
	before := snapshot(t, s.Dir)
	ctx := context.Background()
	for name, o := range map[string]Owner{
		"zero":   {},
		"covers": {kind: KindCovers, id: 1},
		"id 0":   {kind: KindAvatars, id: 0},
		"kind":   {kind: "other", id: 1},
	} {
		if err := s.CleanOwner(ctx, o, ""); err != ErrInvalidOwner {
			t.Fatalf("CleanOwner(%s) = %v", name, err)
		}
	}
	sameTree(t, "store", before, snapshot(t, s.Dir))

	noop := func(OwnerEntries) error { t.Fatalf("callback ran"); return nil }
	for _, c := range []struct {
		kind string
		max  int
	}{{KindCovers, 10}, {"other", 10}, {"", 10}, {KindAvatars, 0}, {KindAvatars, -1}, {KindAvatars, 1001}} {
		stats, err := s.ScanOwners(ctx, c.kind, c.max, noop)
		if err != ErrInvalidOwner || stats != (ScanStats{}) {
			t.Fatalf("ScanOwners(%q, %d) = %+v %v", c.kind, c.max, stats, err)
		}
	}
	for _, n := range []int{1, 1000} {
		if _, err := s.ScanOwners(ctx, KindAvatars, n, func(OwnerEntries) error { return nil }); err != nil {
			t.Fatalf("ScanOwners max %d: %v", n, err)
		}
	}
}

func TestCleanOwnerKeepsCurrentRemovesRest(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner := avatarOwner(t, 1)
	// Missing namespaces are success.
	if err := s.CleanOwner(ctx, owner, ""); err != nil {
		t.Fatalf("no avatars folder: %v", err)
	}
	put(t, s.Dir, "avatars/2/other.png", "two")
	if err := s.CleanOwner(ctx, owner, ""); err != nil {
		t.Fatalf("no owner folder: %v", err)
	}

	put(t, s.Dir, "avatars/1/cur.png", "current")
	put(t, s.Dir, "avatars/1/old.png", "old")
	put(t, s.Dir, "avatars/1/orphan", "orphan")
	put(t, s.Dir, "avatars/1/.tmp-deadbeef", "partial")
	if err := s.CleanOwner(ctx, owner, "avatars/1/cur.png"); err != nil {
		t.Fatalf("clean with keep: %v", err)
	}
	for _, gone := range []string{"old.png", "orphan", ".tmp-deadbeef"} {
		if exists(s.Dir, "avatars/1/"+gone) {
			t.Fatalf("%s survived", gone)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(s.Dir, "avatars/1/cur.png")); string(got) != "current" {
		t.Fatalf("keep changed: %q", got)
	}
	if err := s.CleanOwner(ctx, owner, "avatars/1/cur.png"); err != nil {
		t.Fatalf("second clean: %v", err)
	}
	// With nothing to keep, every file goes, then the empty folder.
	if err := s.CleanOwner(ctx, owner, ""); err != nil {
		t.Fatalf("clean all: %v", err)
	}
	if exists(s.Dir, "avatars/1") {
		t.Fatalf("empty owner folder survived")
	}
	if !exists(s.Dir, "avatars/2/other.png") {
		t.Fatalf("another owner's file went")
	}
}

func TestCleanOwnerSameNameKeep(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		keep string
		kept bool
	}{
		{"avatars/2/x.png", false},
		{"covers/1/x.png", false},
		{"avatars/1/x.png", true},
		{"avatars/1/x.png/y", false},
		{"avatars/01/x.png", false},
		{"avatars/1/.x.png", false},
		{"x.png", false},
	} {
		s := testStore(t)
		put(t, s.Dir, "avatars/1/x.png", "mine")
		put(t, s.Dir, "avatars/2/x.png", "two")
		put(t, s.Dir, "covers/1/x.png", "cover")
		two := snapshot(t, filepath.Join(s.Dir, "avatars/2"))
		covers := snapshot(t, filepath.Join(s.Dir, "covers"))
		if err := s.CleanOwner(ctx, avatarOwner(t, 1), tc.keep); err != nil {
			t.Fatalf("keep %q: %v", tc.keep, err)
		}
		if got := exists(s.Dir, "avatars/1/x.png"); got != tc.kept {
			t.Fatalf("keep %q: owner 1 file present = %v", tc.keep, got)
		}
		sameTree(t, "avatars/2", two, snapshot(t, filepath.Join(s.Dir, "avatars/2")))
		sameTree(t, "covers", covers, snapshot(t, filepath.Join(s.Dir, "covers")))
	}
}

func TestCleanOwnerStaysInItsNamespace(t *testing.T) {
	s := testStore(t)
	put(t, s.Dir, "avatars/1/a.png", "a")
	put(t, s.Dir, "avatars/10/a.png", "ten")
	put(t, s.Dir, "avatars/11/a.png", "eleven")
	put(t, s.Dir, "covers/1/a.png", "cover1")
	put(t, s.Dir, "covers/10/a.png", "cover10")
	ten := snapshot(t, filepath.Join(s.Dir, "avatars/10"))
	eleven := snapshot(t, filepath.Join(s.Dir, "avatars/11"))
	covers := snapshot(t, filepath.Join(s.Dir, "covers"))
	if err := s.CleanOwner(context.Background(), avatarOwner(t, 1), ""); err != nil {
		t.Fatalf("clean: %v", err)
	}
	if exists(s.Dir, "avatars/1") {
		t.Fatalf("owner 1 not cleaned")
	}
	sameTree(t, "avatars/10", ten, snapshot(t, filepath.Join(s.Dir, "avatars/10")))
	sameTree(t, "avatars/11", eleven, snapshot(t, filepath.Join(s.Dir, "avatars/11")))
	sameTree(t, "covers", covers, snapshot(t, filepath.Join(s.Dir, "covers")))
}

func TestStaticSymlinkOwnerFolderIsRefused(t *testing.T) {
	ctx := context.Background()
	outside := t.TempDir()
	for name, target := range map[string]string{"sibling": "2", "outside": outside} {
		s := testStore(t)
		put(t, s.Dir, "avatars/2/x.png", "two")
		put(t, outside, "x.png", "outside")
		symlink(t, s.Dir, target, "avatars/1")
		put(t, s.Dir, "covers/3/z.png", "cover")
		all := snapshot(t, s.Dir)
		out := snapshot(t, outside)

		if err := s.CleanOwner(ctx, avatarOwner(t, 1), ""); err != ErrUnsafeOwner {
			t.Fatalf("%s: CleanOwner = %v", name, err)
		}
		if err := s.Put(ctx, "avatars/1/new.png", strings.NewReader("x"), 1, "image/png"); err != ErrUnsafeOwner {
			t.Fatalf("%s: Put = %v", name, err)
		}
		if err := s.Delete(ctx, "avatars/1/x.png"); err != ErrUnsafeOwner {
			t.Fatalf("%s: Delete = %v", name, err)
		}
		if f, err := s.Open("avatars/1/x.png"); f != nil || err != ErrUnsafeOwner {
			t.Fatalf("%s: Open = %v %v", name, f, err)
		}
		if ok, err := s.Exists(ctx, "avatars/1/x.png"); ok || err != ErrUnsafeOwner {
			t.Fatalf("%s: Exists = %v %v", name, ok, err)
		}
		sameTree(t, name+" store", all, snapshot(t, s.Dir))
		sameTree(t, name+" outside", out, snapshot(t, outside))
	}
}

func TestStaticSymlinkKindFolderIsRefused(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	put(t, s.Dir, "covers/1/x.png", "cover")
	symlink(t, s.Dir, "covers", "avatars")
	before := snapshot(t, s.Dir)
	if err := s.CleanOwner(ctx, avatarOwner(t, 1), ""); err != ErrUnsafeOwner {
		t.Fatalf("CleanOwner = %v", err)
	}
	if err := s.Put(ctx, "avatars/1/new.png", strings.NewReader("x"), 1, "image/png"); err != ErrUnsafeOwner {
		t.Fatalf("Put = %v", err)
	}
	if err := s.Delete(ctx, "avatars/1/x.png"); err != ErrUnsafeOwner {
		t.Fatalf("Delete = %v", err)
	}
	if _, err := s.Open("avatars/1/x.png"); err != ErrUnsafeOwner {
		t.Fatalf("Open = %v", err)
	}
	if _, err := s.ScanOwners(ctx, KindAvatars, 10, func(OwnerEntries) error { return nil }); err != ErrUnsafeOwner {
		t.Fatalf("ScanOwners = %v", err)
	}
	sameTree(t, "store", before, snapshot(t, s.Dir))
}

func TestSymlinkEntriesAreUnlinkedNotFollowed(t *testing.T) {
	outside := t.TempDir()
	put(t, outside, "file.txt", "outside file")
	put(t, outside, "dir/inner.txt", "outside inner")
	s := testStore(t)
	put(t, s.Dir, "avatars/2/b.png", "two")
	put(t, s.Dir, "avatars/2/sub/c.png", "nested")
	put(t, s.Dir, "avatars/1/real.png", "real")
	symlink(t, s.Dir, "../2/b.png", "avatars/1/to-file")
	symlink(t, s.Dir, "../2", "avatars/1/to-dir")
	symlink(t, s.Dir, filepath.Join(outside, "file.txt"), "avatars/1/to-outside-file")
	symlink(t, s.Dir, filepath.Join(outside, "dir"), "avatars/1/to-outside-dir")
	two := snapshot(t, filepath.Join(s.Dir, "avatars/2"))
	out := snapshot(t, outside)

	if err := s.CleanOwner(context.Background(), avatarOwner(t, 1), ""); err != nil {
		t.Fatalf("clean: %v", err)
	}
	if exists(s.Dir, "avatars/1") {
		t.Fatalf("owner folder survived: symlinks to directories are unlinked, not nested folders")
	}
	sameTree(t, "avatars/2", two, snapshot(t, filepath.Join(s.Dir, "avatars/2")))
	sameTree(t, "outside", out, snapshot(t, outside))
}

func TestRaceStepsAreRefused(t *testing.T) {
	s := testStore(t)
	put(t, s.Dir, "avatars/1/a.png", "one")
	put(t, s.Dir, "avatars/2/x.png", "two")
	put(t, s.Dir, "covers/3/z.png", "cover")
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	kindRoot, err := root.OpenRoot("avatars")
	if err != nil {
		t.Fatal(err)
	}
	defer kindRoot.Close()
	two := snapshot(t, filepath.Join(s.Dir, "avatars/2"))

	// Directory level: Lstat the real folder, then swap it for a symlink.
	fi, err := lstatDir(kindRoot, "1", false)
	if err != nil {
		t.Fatalf("lstatDir: %v", err)
	}
	if err := os.Rename(filepath.Join(s.Dir, "avatars/1"), filepath.Join(s.Dir, "avatars/moved")); err != nil {
		t.Fatal(err)
	}
	symlink(t, s.Dir, "2", "avatars/1")
	if h, err := openVerifiedDir(kindRoot, "1", fi); err != ErrUnsafeOwner || h != nil {
		t.Fatalf("symlink swap: %v %v", h, err)
	}
	// ...or for a different real folder.
	if err := os.Remove(filepath.Join(s.Dir, "avatars/1")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(s.Dir, "avatars/1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if h, err := openVerifiedDir(kindRoot, "1", fi); err != ErrUnsafeOwner || h != nil {
		t.Fatalf("folder swap: %v %v", h, err)
	}
	// A folder that vanished is an open failure, not a swap.
	if err := os.Remove(filepath.Join(s.Dir, "avatars/1")); err != nil {
		t.Fatal(err)
	}
	if h, err := openVerifiedDir(kindRoot, "1", fi); err != errDirIO || h != nil {
		t.Fatalf("vanished folder: %v %v", h, err)
	}
	if _, err := lstatDir(kindRoot, "1", false); err != errNoDir {
		t.Fatalf("missing folder: %v", err)
	}
	sameTree(t, "avatars/2", two, snapshot(t, filepath.Join(s.Dir, "avatars/2")))

	// Entry level, in a fresh owner folder.
	put(t, s.Dir, "avatars/5/f.png", "file")
	put(t, s.Dir, "avatars/5/sibling.png", "sibling")
	owner5, err := kindRoot.OpenRoot("5")
	if err != nil {
		t.Fatal(err)
	}
	defer owner5.Close()
	efi, err := lstatEntry(owner5, "f.png")
	if err != nil {
		t.Fatalf("lstatEntry: %v", err)
	}
	if err := os.Rename(filepath.Join(s.Dir, "avatars/5/f.png"), filepath.Join(s.Dir, "avatars/5/f.moved")); err != nil {
		t.Fatal(err)
	}
	// A symlink to a sibling stays inside the folder: the identity check
	// refuses it.
	symlink(t, s.Dir, "sibling.png", "avatars/5/f.png")
	if f, err := openVerifiedEntry(owner5, "f.png", efi); f != nil || err != errNotFound {
		t.Fatalf("sibling symlink: %v %v", f, err)
	}
	// A symlink out to another namespace escapes the owner handle, which
	// os.Root refuses; either way nothing is read.
	if err := os.Remove(filepath.Join(s.Dir, "avatars/5/f.png")); err != nil {
		t.Fatal(err)
	}
	symlink(t, s.Dir, "../../covers/3/z.png", "avatars/5/f.png")
	if f, err := openVerifiedEntry(owner5, "f.png", efi); f != nil || (err != errNotFound && err != errReadFailed) {
		t.Fatalf("escaping symlink: %v %v", f, err)
	}
	// A different regular file in its place has another identity.
	if err := os.Remove(filepath.Join(s.Dir, "avatars/5/f.png")); err != nil {
		t.Fatal(err)
	}
	put(t, s.Dir, "avatars/5/f.png", "impostor")
	if f, err := openVerifiedEntry(owner5, "f.png", efi); f != nil || err != errNotFound {
		t.Fatalf("replaced file: %v %v", f, err)
	}
}

func TestCreateModeRefusesSymlinkAfterMkdir(t *testing.T) {
	s := testStore(t)
	put(t, s.Dir, "avatars/2/x.png", "two")
	symlink(t, s.Dir, "2", "avatars/1")
	two := snapshot(t, filepath.Join(s.Dir, "avatars/2"))
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	kindRoot, err := root.OpenRoot("avatars")
	if err != nil {
		t.Fatal(err)
	}
	defer kindRoot.Close()
	// Mkdir fails with EEXIST on the symlink; the Lstat after it refuses.
	if fi, err := lstatDir(kindRoot, "1", true); fi != nil || err != ErrUnsafeOwner {
		t.Fatalf("create over a symlink: %v %v", fi, err)
	}
	sameTree(t, "avatars/2", two, snapshot(t, filepath.Join(s.Dir, "avatars/2")))
	// A name that is a plain file is no folder either.
	put(t, s.Dir, "avatars/3", "file")
	if _, err := lstatDir(kindRoot, "3", true); err != ErrUnsafeOwner {
		t.Fatalf("create over a file: %v", err)
	}
	// A new name is made.
	if fi, err := lstatDir(kindRoot, "4", true); err != nil || !fi.IsDir() {
		t.Fatalf("create: %v %v", fi, err)
	}
}

// stressSwap flips target between a real folder, a symlink to inside and a
// symlink to outside while Put, CleanOwner and Open loop on avatars/1/x.png.
func stressSwap(t *testing.T, s *LocalStore, target, inside, outside string, secrets map[string]string) {
	t.Helper()
	targetPath := filepath.Join(s.Dir, filepath.FromSlash(target))
	before := map[string]map[string]string{}
	for name := range secrets {
		before[name] = snapshot(t, name)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	ctx := context.Background()
	var leaked sync.Map
	worker := func(fn func()) {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				fn()
			}
		}
	}
	wg.Add(3)
	go worker(func() {
		_ = s.Put(ctx, "avatars/1/x.png", strings.NewReader("mine"), 4, "image/png")
	})
	go worker(func() {
		_ = s.CleanOwner(ctx, avatarOwner(t, 1), "")
	})
	go worker(func() {
		f, err := s.Open("avatars/1/x.png")
		if err != nil {
			return
		}
		b, _ := io.ReadAll(f)
		f.Close()
		if string(b) != "mine" {
			leaked.Store(string(b), true)
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for i := 0; i < 2000 && time.Now().Before(deadline); i++ {
		_ = os.RemoveAll(targetPath)
		switch i % 3 {
		case 0:
			_ = os.Mkdir(targetPath, 0o755)
		case 1:
			_ = os.Symlink(inside, targetPath)
		case 2:
			_ = os.Symlink(outside, targetPath)
		}
	}
	close(stop)
	wg.Wait()
	leaked.Range(func(k, _ any) bool {
		t.Errorf("Open returned bytes from outside the owner folder: %q", k)
		return true
	})
	for name := range secrets {
		sameTree(t, name, before[name], snapshot(t, name))
	}
}

func TestSwapStressOwnerFolder(t *testing.T) {
	s := testStore(t)
	outside := t.TempDir()
	put(t, s.Dir, "avatars/2/x.png", "owner two")
	put(t, s.Dir, "covers/1/x.png", "cover one")
	put(t, outside, "x.png", "outside")
	stressSwap(t, s, "avatars/1", "2", outside, map[string]string{
		filepath.Join(s.Dir, "avatars/2"): "",
		filepath.Join(s.Dir, "covers"):    "",
		outside:                           "",
	})
}

func TestSwapStressKindFolder(t *testing.T) {
	s := testStore(t)
	outside := t.TempDir()
	put(t, s.Dir, "covers/1/x.png", "cover one")
	put(t, outside, "1/x.png", "outside")
	stressSwap(t, s, "avatars", "covers", outside, map[string]string{
		filepath.Join(s.Dir, "covers"): "",
		outside:                        "",
	})
}

func TestCleanOwnerNestedFolderIsLeftAlone(t *testing.T) {
	s := testStore(t)
	put(t, s.Dir, "avatars/1/a.png", "a")
	put(t, s.Dir, "avatars/1/b.png", "b")
	put(t, s.Dir, "avatars/1/nested/inner.png", "inner")
	put(t, s.Dir, "avatars/2/keep.png", "two")
	symlink(t, s.Dir, "../2", "avatars/1/link-to-dir")
	two := snapshot(t, filepath.Join(s.Dir, "avatars/2"))
	err := s.CleanOwner(context.Background(), avatarOwner(t, 1), "")
	if err != ErrUnexpectedEntry {
		t.Fatalf("CleanOwner = %v, want ErrUnexpectedEntry", err)
	}
	noLeak(t, err, s.Dir)
	for _, gone := range []string{"a.png", "b.png", "link-to-dir"} {
		if exists(s.Dir, "avatars/1/"+gone) {
			t.Fatalf("%s survived", gone)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(s.Dir, "avatars/1/nested/inner.png")); string(got) != "inner" {
		t.Fatalf("nested content changed: %q", got)
	}
	sameTree(t, "avatars/2", two, snapshot(t, filepath.Join(s.Dir, "avatars/2")))
}

func fillOwner(t *testing.T, dir string, id, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		put(t, dir, fmt.Sprintf("avatars/%d/f%04d.png", id, i), "x")
	}
}

func TestCleanOwnerBatches(t *testing.T) {
	ctx := context.Background()
	t.Run("exactly 256 finish in one call", func(t *testing.T) {
		s := testStore(t)
		fillOwner(t, s.Dir, 1, CleanBatch)
		if err := s.CleanOwner(ctx, avatarOwner(t, 1), ""); err != nil {
			t.Fatalf("clean: %v", err)
		}
		if exists(s.Dir, "avatars/1") {
			t.Fatalf("folder survived")
		}
	})
	t.Run("257 take two calls", func(t *testing.T) {
		s := testStore(t)
		fillOwner(t, s.Dir, 1, CleanBatch+1)
		err := s.CleanOwner(ctx, avatarOwner(t, 1), "")
		if err != ErrMoreRemain {
			t.Fatalf("first call = %v, want ErrMoreRemain", err)
		}
		noLeak(t, err, s.Dir)
		entries, _ := os.ReadDir(filepath.Join(s.Dir, "avatars/1"))
		if len(entries) != 1 {
			t.Fatalf("%d entries left after one batch, want 1", len(entries))
		}
		if err := s.CleanOwner(ctx, avatarOwner(t, 1), ""); err != nil {
			t.Fatalf("second call: %v", err)
		}
		if exists(s.Dir, "avatars/1") {
			t.Fatalf("folder survived")
		}
	})
	t.Run("255 orphans and the keep finish in one call", func(t *testing.T) {
		s := testStore(t)
		fillOwner(t, s.Dir, 1, CleanBatch-1)
		put(t, s.Dir, "avatars/1/current.png", "current")
		if err := s.CleanOwner(ctx, avatarOwner(t, 1), "avatars/1/current.png"); err != nil {
			t.Fatalf("clean: %v", err)
		}
		entries, _ := os.ReadDir(filepath.Join(s.Dir, "avatars/1"))
		if len(entries) != 1 || entries[0].Name() != "current.png" {
			t.Fatalf("left %d entries", len(entries))
		}
	})
}

func TestCleanOwnerContext(t *testing.T) {
	s := testStore(t)
	fillOwner(t, s.Dir, 1, 200)
	before := snapshot(t, s.Dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.CleanOwner(ctx, avatarOwner(t, 1), "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CleanOwner = %v, want context.Canceled", err)
	}
	noLeak(t, err, s.Dir)
	sameTree(t, "store", before, snapshot(t, s.Dir))
	if err := s.CleanOwner(context.Background(), avatarOwner(t, 1), ""); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if exists(s.Dir, "avatars/1") {
		t.Fatalf("folder survived")
	}
}

// fakeEntry is a DirEntry that only has a name.
type fakeEntry struct{ name string }

func (e fakeEntry) Name() string               { return e.name }
func (e fakeEntry) IsDir() bool                { return false }
func (e fakeEntry) Type() fs.FileMode          { return 0 }
func (e fakeEntry) Info() (fs.FileInfo, error) { return nil, errors.New("no info") }

type fakeStep struct {
	n   int
	err error
}

// fakeReader returns n entries (never more than asked for) and err for each
// step, then io.EOF.
type fakeReader struct {
	steps  []fakeStep
	calls  int
	served int
	wants  []int
}

func (f *fakeReader) ReadDir(n int) ([]fs.DirEntry, error) {
	f.wants = append(f.wants, n)
	if f.calls >= len(f.steps) {
		return nil, io.EOF
	}
	st := f.steps[f.calls]
	f.calls++
	cnt := st.n
	if cnt > n {
		cnt = n
	}
	out := make([]fs.DirEntry, cnt)
	for i := range out {
		out[i] = fakeEntry{name: fmt.Sprintf("e%d", f.served)}
		f.served++
	}
	return out, st.err
}

func ones(n int) []fakeStep {
	steps := make([]fakeStep, n)
	for i := range steps {
		steps[i] = fakeStep{n: 1}
	}
	return steps
}

func TestReadDirUpTo(t *testing.T) {
	const limit = 5
	cases := []struct {
		name    string
		steps   []fakeStep
		want    int
		more    bool
		maxSeen int
	}{
		{"one per call, exactly limit", ones(limit), limit, false, limit},
		{"one per call, limit+1", ones(limit + 1), limit, true, limit + 1},
		{"one per call, many more", ones(40), limit, true, limit + 1},
		{"uneven chunks, exactly limit", []fakeStep{{n: 1}, {n: 3}, {n: 1}}, limit, false, limit},
		{"uneven chunks, more", []fakeStep{{n: 1}, {n: 3}, {n: 2}, {n: 4}}, limit, true, limit + 1},
		{"fewer than limit", ones(2), 2, false, 2},
		{"empty", nil, 0, false, 0},
		{"last entries come with EOF", []fakeStep{{n: 2}, {n: 2, err: io.EOF}}, 4, false, 4},
		{"limit+1 entries come with EOF", []fakeStep{{n: 3}, {n: 3, err: io.EOF}}, limit, true, limit + 1},
	}
	for _, tc := range cases {
		f := &fakeReader{steps: tc.steps}
		entries, more, err := readDirUpTo(f, limit)
		if err != nil || len(entries) != tc.want || more != tc.more {
			t.Fatalf("%s: %d entries, more %v, err %v", tc.name, len(entries), more, err)
		}
		if f.served > tc.maxSeen {
			t.Fatalf("%s: pulled %d entries, more than %d", tc.name, f.served, tc.maxSeen)
		}
		for i, e := range entries {
			if e.Name() != fmt.Sprintf("e%d", i) {
				t.Fatalf("%s: entry %d is %q", tc.name, i, e.Name())
			}
		}
	}

	// A reader that returns nothing and no error cannot spin the loop.
	f := &fakeReader{steps: []fakeStep{{n: 0}, {n: 3}}}
	entries, more, err := readDirUpTo(f, limit)
	if err != nil || len(entries) != 0 || more || f.calls != 1 {
		t.Fatalf("empty read: %d %v %v after %d calls", len(entries), more, err, f.calls)
	}

	// An error drops every entry, so no caller acts on a partial read.
	boom := errors.New("boom")
	for name, steps := range map[string][]fakeStep{
		"entries with a non-EOF error": {{n: 2, err: boom}},
		"failure on the second call":   {{n: 3}, {n: 0, err: boom}},
		"failure after a full chunk":   {{n: limit}, {n: 0, err: boom}},
	} {
		entries, more, err := readDirUpTo(&fakeReader{steps: steps}, limit)
		if err != boom || len(entries) != 0 || more {
			t.Fatalf("%s: %d entries, more %v, err %v", name, len(entries), more, err)
		}
	}
}

func TestScanOwners(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	put(t, s.Dir, "avatars/1/a.png", "a")
	put(t, s.Dir, "avatars/2/b.png", "b")
	put(t, s.Dir, "avatars/2/c.png", "c")
	put(t, s.Dir, "avatars/3/d.png", "d")
	put(t, s.Dir, "avatars/3/e.png", "e")
	put(t, s.Dir, "avatars/3/f.png", "f")
	put(t, s.Dir, "avatars/10/empty-placeholder", "x")
	if err := os.Remove(filepath.Join(s.Dir, "avatars/10/empty-placeholder")); err != nil {
		t.Fatal(err)
	}
	// Names that are not canonical ids, plus a plain file.
	for _, n := range []string{"01", "abc", "0"} {
		if err := os.MkdirAll(filepath.Join(s.Dir, "avatars", n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	put(t, s.Dir, "avatars/note.txt", "note")
	// A symlinked owner and a plain file with an id for a name.
	symlink(t, s.Dir, "1", "avatars/5")
	put(t, s.Dir, "avatars/6", "file")
	before := snapshot(t, s.Dir)

	got := map[uint64]OwnerEntries{}
	calls := 0
	stats, err := s.ScanOwners(ctx, KindAvatars, 2, func(oe OwnerEntries) error {
		calls++
		got[oe.Owner] = oe
		return nil
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if stats.SkippedNames != 4 || stats.Owners != calls || calls != len(got) {
		t.Fatalf("stats %+v, %d calls, %d owners", stats, calls, len(got))
	}
	for _, id := range []uint64{1, 2, 3, 10, 5, 6} {
		if _, ok := got[id]; !ok {
			t.Fatalf("owner %d not reported: %+v", id, got)
		}
	}
	if len(got) != 6 {
		t.Fatalf("reported %d owners: %+v", len(got), got)
	}
	if e := got[1]; e.Unsafe || e.Truncated || !reflect.DeepEqual(e.Keys, []string{"avatars/1/a.png"}) {
		t.Fatalf("owner 1: %+v", e)
	}
	// Exactly maxNames entries is not truncated; one more is.
	if e := got[2]; e.Unsafe || e.Truncated || len(e.Keys) != 2 {
		t.Fatalf("owner 2: %+v", e)
	}
	if e := got[3]; e.Unsafe || !e.Truncated || len(e.Keys) != 2 {
		t.Fatalf("owner 3: %+v", e)
	}
	if e := got[10]; e.Unsafe || e.Truncated || len(e.Keys) != 0 {
		t.Fatalf("owner 10: %+v", e)
	}
	for _, id := range []uint64{5, 6} {
		if e := got[id]; !e.Unsafe || len(e.Keys) != 0 || e.Truncated {
			t.Fatalf("owner %d should be unsafe with no keys: %+v", id, e)
		}
	}
	sameTree(t, "store", before, snapshot(t, s.Dir))

	// An error from fn stops the scan and comes back unchanged.
	stop := errors.New("stop here")
	calls = 0
	stats, err = s.ScanOwners(ctx, KindAvatars, 2, func(OwnerEntries) error {
		calls++
		return stop
	})
	if err != stop || calls != 1 || stats.Owners != 1 {
		t.Fatalf("fn error: %v after %d calls, stats %+v", err, calls, stats)
	}

	// A cancelled context returns context.Canceled with the stats so far.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	stats, err = s.ScanOwners(cctx, KindAvatars, 2, func(OwnerEntries) error { return nil })
	if err != context.Canceled || stats.Owners != 0 {
		t.Fatalf("cancelled scan: %+v %v", stats, err)
	}
}

func TestScanOwnersMissingKindFolder(t *testing.T) {
	s := testStore(t)
	stats, err := s.ScanOwners(context.Background(), KindAvatars, 5, func(OwnerEntries) error {
		t.Fatalf("callback ran")
		return nil
	})
	if err != nil || stats != (ScanStats{}) {
		t.Fatalf("missing folder: %+v %v", stats, err)
	}
}

func TestScanOwnersTruncationBoundary(t *testing.T) {
	for _, tc := range []struct {
		files, max int
		truncated  bool
	}{{3, 3, false}, {4, 3, true}, {1, 1, false}, {2, 1, true}} {
		s := testStore(t)
		fillOwner(t, s.Dir, 1, tc.files)
		var got OwnerEntries
		if _, err := s.ScanOwners(context.Background(), KindAvatars, tc.max, func(oe OwnerEntries) error {
			got = oe
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if got.Truncated != tc.truncated || len(got.Keys) != min(tc.files, tc.max) {
			t.Fatalf("%d files, max %d: %+v", tc.files, tc.max, got)
		}
	}
}

func TestScanOwnersUnreadableOwnerGoesOn(t *testing.T) {
	skipIfRoot(t)
	s := testStore(t)
	put(t, s.Dir, "avatars/1/a.png", "a")
	put(t, s.Dir, "avatars/2/b.png", "b")
	chmod(t, filepath.Join(s.Dir, "avatars/1"), 0o000)
	got := map[uint64]OwnerEntries{}
	stats, err := s.ScanOwners(context.Background(), KindAvatars, 10, func(oe OwnerEntries) error {
		got[oe.Owner] = oe
		return nil
	})
	if err != nil || stats.Owners != 2 {
		t.Fatalf("scan: %+v %v", stats, err)
	}
	if e := got[1]; !e.Unsafe || len(e.Keys) != 0 {
		t.Fatalf("unreadable owner: %+v", e)
	}
	if e := got[2]; e.Unsafe || len(e.Keys) != 1 {
		t.Fatalf("next owner: %+v", e)
	}
}

func TestOpenAndExistsOnlyRegularFiles(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	put(t, s.Dir, "avatars/1/real.png", "real bytes")
	put(t, s.Dir, "avatars/2/secret.png", "secret")
	put(t, s.Dir, "covers/3/z.png", "cover")
	symlink(t, s.Dir, "../2/secret.png", "avatars/1/to-secret")
	symlink(t, s.Dir, "../../covers/3/z.png", "avatars/1/to-cover")
	if err := os.MkdirAll(filepath.Join(s.Dir, "avatars/1/sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(s.Dir, "avatars/1/pipe.png"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := s.Open("avatars/1/real.png")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	b, _ := io.ReadAll(f)
	f.Close()
	if string(b) != "real bytes" {
		t.Fatalf("content %q", b)
	}
	if f, err := s.Open("covers/3/z.png"); err != nil {
		t.Fatalf("open a cover: %v", err)
	} else {
		f.Close()
	}
	if ok, err := s.Exists(ctx, "avatars/1/real.png"); !ok || err != nil {
		t.Fatalf("exists: %v %v", ok, err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, name := range []string{"to-secret", "to-cover", "sub", "pipe.png", "missing.png"} {
			key := "avatars/1/" + name
			if f, err := s.Open(key); f != nil || err != errNotFound {
				t.Errorf("Open(%s) = %v %v, want errNotFound", name, f, err)
			}
			if ok, err := s.Exists(ctx, key); ok || err != nil {
				t.Errorf("Exists(%s) = %v %v, want false, nil", name, ok, err)
			}
		}
		if f, err := s.Open("avatars/9/none.png"); f != nil || err != errNotFound {
			t.Errorf("Open in a missing folder = %v %v", f, err)
		}
		if ok, err := s.Exists(ctx, "avatars/9/none.png"); ok || err != nil {
			t.Errorf("Exists in a missing folder = %v %v", ok, err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Open or Exists blocked (a FIFO?)")
	}
}

func TestDeleteUnlinksSymlinkNotTarget(t *testing.T) {
	s := testStore(t)
	put(t, s.Dir, "avatars/2/secret.png", "secret")
	symlink(t, s.Dir, "../2/secret.png", "avatars/1/link.png")
	if err := s.Delete(context.Background(), "avatars/1/link.png"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if exists(s.Dir, "avatars/1/link.png") {
		t.Fatalf("link survived")
	}
	if !exists(s.Dir, "avatars/2/secret.png") {
		t.Fatalf("target went")
	}
	// A missing folder is success too.
	if err := s.Delete(context.Background(), "avatars/9/none.png"); err != nil {
		t.Fatalf("delete in a missing folder: %v", err)
	}
}

func TestErrorsAreFixedTextAndExact(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	put(t, s.Dir, "avatars/2/x.png", "two")
	symlink(t, s.Dir, "2", "avatars/1")
	put(t, s.Dir, "avatars/3/a.png", "a")
	put(t, s.Dir, "avatars/3/nested/b.png", "b")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	put(t, s.Dir, "avatars/4/a.png", "a")

	checks := []struct {
		name string
		got  error
		want error
	}{
		{"invalid key", s.Put(ctx, "../x", strings.NewReader("x"), 1, ""), ErrInvalidKey},
		{"invalid owner", s.CleanOwner(ctx, Owner{}, ""), ErrInvalidOwner},
		{"symlinked owner", s.CleanOwner(ctx, avatarOwner(t, 1), ""), ErrUnsafeOwner},
		{"nested folder", s.CleanOwner(ctx, avatarOwner(t, 3), ""), ErrUnexpectedEntry},
		{"cancelled", s.CleanOwner(cancelled, avatarOwner(t, 4), ""), context.Canceled},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Fatalf("%s: got %v, want %v", c.name, c.got, c.want)
		}
		noLeak(t, c.got, s.Dir)
	}
}

func TestPermissionFailuresAreFixedText(t *testing.T) {
	skipIfRoot(t)
	ctx := context.Background()
	for _, mode := range []os.FileMode{0o000, 0o333, 0o444, 0o555} {
		t.Run(mode.String(), func(t *testing.T) {
			s := testStore(t)
			put(t, s.Dir, "avatars/1/a.png", "a")
			put(t, s.Dir, "avatars/1/b.png", "b")
			before := snapshot(t, s.Dir)
			owner := filepath.Join(s.Dir, "avatars/1")
			chmod(t, owner, mode)
			err := s.CleanOwner(ctx, avatarOwner(t, 1), "")
			if err != ErrCleanupIncomplete {
				t.Fatalf("CleanOwner = %v, want ErrCleanupIncomplete", err)
			}
			noLeak(t, err, s.Dir)
			if mode == 0o000 {
				if err := s.Put(ctx, "avatars/1/new.png", strings.NewReader("x"), 1, ""); err != errStoreFailed {
					t.Fatalf("Put = %v", err)
				}
				if err := s.Delete(ctx, "avatars/1/a.png"); err != errRemoveFailed {
					t.Fatalf("Delete = %v", err)
				}
				if f, err := s.Open("avatars/1/a.png"); f != nil || err != errReadFailed {
					t.Fatalf("Open = %v %v", f, err)
				}
				if ok, err := s.Exists(ctx, "avatars/1/a.png"); ok || err != errReadFailed {
					t.Fatalf("Exists = %v %v", ok, err)
				}
			}
			if err := os.Chmod(owner, 0o755); err != nil {
				t.Fatal(err)
			}
			sameTree(t, "store", before, snapshot(t, s.Dir))
		})
	}
}

func TestScanOwnersUnreadableKindFolder(t *testing.T) {
	skipIfRoot(t)
	s := testStore(t)
	put(t, s.Dir, "avatars/1/a.png", "a")
	chmod(t, filepath.Join(s.Dir, "avatars"), 0o000)
	stats, err := s.ScanOwners(context.Background(), KindAvatars, 10, func(OwnerEntries) error {
		t.Fatalf("callback ran")
		return nil
	})
	if err != ErrScanFailed || stats != (ScanStats{}) {
		t.Fatalf("scan = %+v %v, want ErrScanFailed", stats, err)
	}
	noLeak(t, err, s.Dir)
}

func TestMissingUploadRootIsFixedText(t *testing.T) {
	s := &LocalStore{Dir: filepath.Join(t.TempDir(), "gone"), URLPrefix: "/uploads"}
	ctx := context.Background()
	if err := s.CleanOwner(ctx, avatarOwner(t, 1), ""); err != ErrCleanupIncomplete {
		t.Fatalf("CleanOwner = %v", err)
	}
	if _, err := s.ScanOwners(ctx, KindAvatars, 5, func(OwnerEntries) error { return nil }); err != ErrScanFailed {
		t.Fatalf("ScanOwners = %v", err)
	}
	if err := s.Put(ctx, "avatars/1/a.png", strings.NewReader("x"), 1, ""); err != errStoreFailed {
		t.Fatalf("Put = %v", err)
	}
	if err := s.Delete(ctx, "avatars/1/a.png"); err != errRemoveFailed {
		t.Fatalf("Delete = %v", err)
	}
	if _, err := s.Open("avatars/1/a.png"); err != errReadFailed {
		t.Fatalf("Open = %v", err)
	}
	if ok, err := s.Exists(ctx, "avatars/1/a.png"); ok || err != errReadFailed {
		t.Fatalf("Exists = %v %v", ok, err)
	}
}
