package pywalk

import (
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"cursorlite/internal/paths"
)

var SkipPythonWalkDirNames = map[string]struct{}{
	".git": {}, ".venv": {}, "venv": {}, "__pycache__": {}, "node_modules": {},
	".mypy_cache": {}, ".tox": {}, ".cursorlite": {},
}

var errWorkspacePyListCap = errors.New("workspace python list cap")

// PythonRelPaths returns sorted workspace-relative paths of .py files for agent context (capped).
func PythonRelPaths(root string, limit int) []string {
	if limit <= 0 {
		limit = 200
	}
	out := make([]string, 0, min(limit, 64))
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if _, skip := SkipPythonWalkDirNames[d.Name()]; skip {
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
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		if len(out) >= limit {
			return errWorkspacePyListCap
		}
		return nil
	})
	if err != nil && !errors.Is(err, errWorkspacePyListCap) {
		sort.Strings(out)
		return out
	}
	sort.Strings(out)
	return out
}

// PythonFingerprints maps workspace-relative paths of .py files to a cheap size+mtime fingerprint.
func PythonFingerprints(root string) (map[string]int64, error) {
	out := make(map[string]int64)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if _, skip := SkipPythonWalkDirNames[d.Name()]; skip {
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
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		st, err := d.Info()
		if err != nil {
			return err
		}
		out[rel] = st.Size() ^ int64(st.ModTime().UnixNano())
		return nil
	})
	return out, err
}

func PythonWorkspaceChanged(before, after map[string]int64) bool {
	if len(before) != len(after) {
		return true
	}
	for k, v := range after {
		if before[k] != v {
			return true
		}
	}
	return false
}
