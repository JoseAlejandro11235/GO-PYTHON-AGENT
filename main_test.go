package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafeRel(t *testing.T) {
	root := t.TempDir()
	t.Run("nested", func(t *testing.T) {
		full, err := safeRel(root, "src/app.py")
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(root, "src", "app.py")
		if filepath.Clean(full) != filepath.Clean(want) {
			t.Fatalf("got %q want %q", full, want)
		}
	})
	t.Run("dot slash", func(t *testing.T) {
		full, err := safeRel(root, "./x")
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(root, "x")
		if filepath.Clean(full) != filepath.Clean(want) {
			t.Fatalf("got %q want %q", full, want)
		}
	})
	t.Run("parent escape", func(t *testing.T) {
		if _, err := safeRel(root, ".."); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("parent prefix", func(t *testing.T) {
		if _, err := safeRel(root, "../secret"); err == nil {
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
	if !underRoot(root, inside) {
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
	if underRoot(root, outPath) {
		t.Error("path outside workspace should be rejected")
	}
}

func TestStripMarkdownJSONFence(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{`{"ok":true}`, `{"ok":true}`},
		{"```json\n{\"ok\":true}\n```", `{"ok":true}`},
		{"```\n{\"a\":1}\n```", `{"a":1}`},
		{"  ```json\n{\"x\":\"y\"}\n```  ", `{"x":"y"}`},
	}
	for _, tt := range tests {
		got := stripMarkdownJSONFence(tt.in)
		if got != tt.want {
			t.Errorf("stripMarkdownJSONFence(%q) = %q; want %q", tt.in, got, tt.want)
		}
	}
}

func TestAgentMaxSteps(t *testing.T) {
	t.Setenv("AGENT_MAX_STEPS", "")
	if n := agentMaxSteps(0); n != 12 {
		t.Errorf("default got %d want 12", n)
	}
	if n := agentMaxSteps(99); n != 15 {
		t.Errorf("cap got %d want 15", n)
	}
	t.Setenv("AGENT_MAX_STEPS", "3")
	if n := agentMaxSteps(0); n != 3 {
		t.Errorf("env default got %d want 3", n)
	}
	if n := agentMaxSteps(5); n != 5 {
		t.Errorf("request got %d want 5", n)
	}
}
