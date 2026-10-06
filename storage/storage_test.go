package storage

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func testStore(t *testing.T) *LocalStore {
	t.Helper()
	s, err := NewLocalStore(t.TempDir(), "/uploads")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s
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
	if err := s.Put(context.Background(), "a/b.png", strings.NewReader(""), 0, "image/png"); err == nil {
		t.Fatalf("put accepted empty upload")
	}
}

func TestURLEscapesSegments(t *testing.T) {
	s := testStore(t)
	// Keys are server-generated, but URL escaping must still be safe.
	if u := s.URL("avatars/1/a b.png"); !strings.Contains(u, "a%20b.png") {
		t.Fatalf("url not escaped: %q", u)
	}
}
