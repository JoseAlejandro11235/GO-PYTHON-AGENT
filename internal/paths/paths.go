package paths

import (
	"errors"
	"path/filepath"
	"strings"
)

func SafeRel(root, rel string) (string, error) {
	rel = filepath.Clean(strings.TrimPrefix(rel, "/"))
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid path")
	}
	full := filepath.Join(root, rel)
	return full, nil
}

func UnderRoot(root, full string) bool {
	rootAbs, err1 := filepath.EvalSymlinks(root)
	fullAbs, err2 := filepath.EvalSymlinks(full)
	if err1 != nil {
		rootAbs = root
	}
	if err2 != nil {
		fullAbs = full
	}
	rootAbs, _ = filepath.Abs(rootAbs)
	fullAbs, _ = filepath.Abs(fullAbs)
	sep := string(filepath.Separator)
	rel, err := filepath.Rel(rootAbs, fullAbs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+sep)
}
