package secret

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	box, err := LoadOrCreate(filepath.Join(t.TempDir(), "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal([]byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("hunter2")) {
		t.Fatal("sealed value contains the plaintext")
	}
	plain, err := box.Open(sealed)
	if err != nil || string(plain) != "hunter2" {
		t.Fatalf("Open = %q, %v", plain, err)
	}

	// Short and tampered values are rejected.
	if _, err := box.Open([]byte("short")); err == nil {
		t.Error("expected error for short input")
	}
	sealed, _ = box.Seal([]byte("x"))
	sealed[len(sealed)-1] ^= 0xff
	if _, err := box.Open(sealed); err == nil {
		t.Error("expected error for tampered input")
	}
}

func TestKeyIsPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	first, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := first.Seal([]byte("x"))
	second, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Open(sealed); err != nil {
		t.Fatalf("second box cannot open value of the first: %v", err)
	}
}
