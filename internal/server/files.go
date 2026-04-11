package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"cursorlite/internal/paths"
)

type treeEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	IsDir    bool   `json:"isDir"`
	Children []any  `json:"children,omitempty"`
}

func handleTree(w http.ResponseWriter, r *http.Request, root string) {
	q := r.URL.Query().Get("path")
	full, err := paths.SafeRel(root, q)
	if err != nil || !paths.UnderRoot(root, full) {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	st, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !st.IsDir() {
		http.Error(w, "not a directory", http.StatusBadRequest)
		return
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]treeEntry, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		childPath := filepath.Join(q, name)
		childPath = strings.TrimPrefix(filepath.Clean(childPath), string(filepath.Separator))
		if q == "" || q == "." {
			childPath = name
		} else {
			childPath = filepath.ToSlash(filepath.Join(q, name))
		}
		te := treeEntry{Name: name, Path: childPath, IsDir: e.IsDir()}
		out = append(out, te)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func handleReadFile(w http.ResponseWriter, r *http.Request, root string) {
	q := r.URL.Query().Get("path")
	full, err := paths.SafeRel(root, q)
	if err != nil || !paths.UnderRoot(root, full) {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	st, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if st.IsDir() {
		http.Error(w, "is directory", http.StatusBadRequest)
		return
	}
	if st.Size() > 10<<20 {
		http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
		return
	}
	data, err := os.ReadFile(full)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(data)
}

type writeBody struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func handleWriteFile(w http.ResponseWriter, r *http.Request, root string) {
	var body writeBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 12<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	full, err := paths.SafeRel(root, body.Path)
	if err != nil || !paths.UnderRoot(root, full) {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(full, []byte(body.Content), 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
