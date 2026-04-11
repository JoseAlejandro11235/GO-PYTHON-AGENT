package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafeRel(t *testing.T) {
	root := t.TempDir()
	t.Run("nested", func(t *testing.T) {
		full, err := SafeRel(root, "src/app.py")
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(root, "src", "app.py")
		if filepath.Clean(full) != filepath.Clean(want) {
			t.Fatalf("got %q want %q", full, want)
		}
	})
	t.Run("dot slash", func(t *testing.T) {
		full, err := SafeRel(root, "./x")
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(root, "x")
		if filepath.Clean(full) != filepath.Clean(want) {
			t.Fatalf("got %q want %q", full, want)
		}
	})
	t.Run("parent escape", func(t *testing.T) {
		if _, err := SafeRel(root, ".."); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("parent prefix", func(t *testing.T) {
		if _, err := SafeRel(root, "../secret"); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestUnderRoot(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "pkg", "sub")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if !UnderRoot(root, inside) {
		t.Error("path inside workspace should be allowed")
	}
	sibling, err := os.MkdirTemp(filepath.Dir(root), "outside-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sibling)
	outPath := filepath.Join(sibling, "x.txt")
	if err := os.WriteFile(outPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if UnderRoot(root, outPath) {
		t.Error("path outside workspace should be rejected")
	}
}
