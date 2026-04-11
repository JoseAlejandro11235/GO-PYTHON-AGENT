package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"cursorlite/internal/python"
)

func handleRunPython(w http.ResponseWriter, r *http.Request, root string) {
	var req python.RunRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	code := strings.TrimRight(req.Code, "\n")
	if code == "" {
		http.Error(w, "code is empty", http.StatusBadRequest)
		return
	}
	if len(req.Stdin) > python.MaxStdinBytes {
		http.Error(w, "stdin too large", http.StatusRequestEntityTooLarge)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), python.RunTimeout())
	defer cancel()
	resp, err := python.RunInWorkspace(ctx, root, code, req.CwdRel, req.Stdin)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
