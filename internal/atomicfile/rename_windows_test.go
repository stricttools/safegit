package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplacementNeverDeletesAnOpenDestination(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "new")
	dst := filepath.Join(root, "current")
	if err := os.WriteFile(src, []byte("new complete record"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("original complete record"), 0600); err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	if err := Rename(src, dst); err == nil {
		held.Close()
		t.Fatal("replaced a destination opened without delete sharing")
	}
	held.Close()
	raw, err := os.ReadFile(dst)
	if err != nil || string(raw) != "original complete record" {
		t.Fatalf("failed replace modified destination: %q %v", raw, err)
	}
	if err := Rename(src, dst); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(dst)
	if err != nil || string(raw) != "new complete record" {
		t.Fatalf("bad replacement: %q %v", raw, err)
	}
}
