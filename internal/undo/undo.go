package undo

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"cursorlite/internal/meta"
	"cursorlite/internal/paths"
	"cursorlite/internal/pywalk"
)

const (
	jsonName        = "last_agent_undo.json"
	maxBytesPerFile = 2 << 20 // skip very large .py files in snapshot
)

type snapshot struct {
	Version int               `json:"version"`
	SavedAt string            `json:"savedAt,omitempty"`
	Paths   map[string]string `json:"paths"`
}

func pythonSnapshotContents(root string) (map[string]string, error) {
	out := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if _, skip := pywalk.SkipPythonWalkDirNames[d.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.ToLower(filepath.Ext(path)) != ".py" {
			return nil
		}
		if !paths.UnderRoot(root, path) {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		st, stErr := d.Info()
		if stErr != nil {
			return stErr
		}
		if st.Size() > maxBytesPerFile {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if !utf8.Valid(b) {
			return nil
		}
		out[rel] = string(b)
		return nil
	})
	return out, err
}

// SaveSnapshot writes the current workspace .py snapshot before an agent run (overwrites previous).
func SaveSnapshot(root string) error {
	paths, err := pythonSnapshotContents(root)
	if err != nil {
		return err
	}
	dir := filepath.Join(root, meta.CursorliteInternalDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	snap := snapshot{
		Version: 1,
		SavedAt: time.Now().UTC().Format(time.RFC3339),
		Paths:   paths,
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, jsonName), b, 0o644)
}

func loadSnapshot(root string) (snapshot, error) {
	p := filepath.Join(root, meta.CursorliteInternalDir, jsonName)
	b, err := os.ReadFile(p)
	if err != nil {
		return snapshot{}, err
	}
	var s snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return snapshot{}, err
	}
	if s.Version != 1 || s.Paths == nil {
		return snapshot{}, errors.New("invalid undo snapshot")
	}
	return s, nil
}

func restoreSnapshot(root string) error {
	s, err := loadSnapshot(root)
	if err != nil {
		return err
	}
	for rel, content := range s.Paths {
		full, relErr := paths.SafeRel(root, rel)
		if relErr != nil || !paths.UnderRoot(root, full) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func HandleAvailable(w http.ResponseWriter, r *http.Request, root string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := filepath.Join(root, meta.CursorliteInternalDir, jsonName)
	st, err := os.Stat(p)
	available := err == nil && !st.IsDir()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"available": available})
}

func HandleUndo(w http.ResponseWriter, r *http.Request, root string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := restoreSnapshot(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "no undo snapshot — run the agent at least once first", http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
