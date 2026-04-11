package server

import (
	"database/sql"
	"io/fs"
	"net/http"

	"cursorlite/internal/agent"
	"cursorlite/internal/prompts"
	"cursorlite/internal/undo"
)

func Register(mux *http.ServeMux, subFS fs.FS, root string, promptDB *sql.DB) {
	mux.Handle("/", http.FileServer(http.FS(subFS)))
	mux.HandleFunc("GET /api/tree", func(w http.ResponseWriter, r *http.Request) {
		handleTree(w, r, root)
	})
	mux.HandleFunc("GET /api/file", func(w http.ResponseWriter, r *http.Request) {
		handleReadFile(w, r, root)
	})
	mux.HandleFunc("PUT /api/file", func(w http.ResponseWriter, r *http.Request) {
		handleWriteFile(w, r, root)
	})
	mux.HandleFunc("POST /api/run-python", func(w http.ResponseWriter, r *http.Request) {
		handleRunPython(w, r, root)
	})
	mux.HandleFunc("GET /ws/run-python", func(w http.ResponseWriter, r *http.Request) {
		handleRunPythonWS(w, r, root)
	})
	mux.HandleFunc("POST /api/agent-code", func(w http.ResponseWriter, r *http.Request) {
		agent.HandleAgentCode(w, r, root, promptDB)
	})
	mux.HandleFunc("GET /api/prompts", func(w http.ResponseWriter, r *http.Request) {
		prompts.HandleAPI(w, r, promptDB)
	})
	mux.HandleFunc("GET /api/agent-undo-available", func(w http.ResponseWriter, r *http.Request) {
		undo.HandleAvailable(w, r, root)
	})
	mux.HandleFunc("POST /api/agent-undo", func(w http.ResponseWriter, r *http.Request) {
		undo.HandleUndo(w, r, root)
	})
}
