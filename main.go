package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"embed"
)

//go:embed static/*
var static embed.FS

func main() {
	root := os.Getenv("WORKSPACE_ROOT")
	if root == "" {
		root = "."
	}
	root, err := filepath.Abs(root)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		log.Fatal(err)
	}

	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	subFS, err := fs.Sub(static, "static")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
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
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		handleChat(w, r)
	})

	log.Printf("cursorlite serving workspace %s on http://0.0.0.0%s", root, addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func safeRel(root, rel string) (string, error) {
	rel = filepath.Clean(strings.TrimPrefix(rel, "/"))
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid path")
	}
	full := filepath.Join(root, rel)
	return full, nil
}

func underRoot(root, full string) bool {
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

type treeEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	IsDir    bool   `json:"isDir"`
	Children []any  `json:"children,omitempty"`
}

func handleTree(w http.ResponseWriter, r *http.Request, root string) {
	q := r.URL.Query().Get("path")
	full, err := safeRel(root, q)
	if err != nil || !underRoot(root, full) {
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
	full, err := safeRel(root, q)
	if err != nil || !underRoot(root, full) {
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
	// Cap file size for editor (10 MiB)
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
	full, err := safeRel(root, body.Path)
	if err != nil || !underRoot(root, full) {
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

type chatReq struct {
	Message string `json:"message"`
}

type chatResp struct {
	Reply string `json:"reply"`
	Error string `json:"error,omitempty"`
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	key := os.Getenv("OPENAI_API_KEY")
	base := os.Getenv("OPENAI_BASE_URL")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	if key == "" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatResp{
			Reply: "",
			Error: "Set OPENAI_API_KEY in the container to enable chat. Example: docker compose with env from .env",
		})
		return
	}
	reply, err := openAIChat(base, key, req.Message)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		_ = json.NewEncoder(w).Encode(chatResp{Error: err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(chatResp{Reply: reply})
}

type openAIRequest struct {
	Model    string              `json:"model"`
	Messages []openAIMessage     `json:"messages"`
	Stream   bool                `json:"stream"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func openAIChat(baseURL, apiKey, userMsg string) (string, error) {
	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		model = "gpt-4o-mini"
	}
	payload, _ := json.Marshal(openAIRequest{
		Model: model,
		Messages: []openAIMessage{
			{Role: "system", Content: "You are a concise coding assistant inside a small web IDE."},
			{Role: "user", Content: userMsg},
		},
		Stream: false,
	})
	req, err := http.NewRequest(http.MethodPost, strings.TrimSuffix(baseURL, "/")+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", errors.New("api: " + string(bytes.TrimSpace(body)))
	}
	var out openAIResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if out.Error != nil {
		return "", errors.New(out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", errors.New("no completion")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}
