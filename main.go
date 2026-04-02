package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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
		handleChat(w, r, root)
	})
	mux.HandleFunc("POST /api/run-python", func(w http.ResponseWriter, r *http.Request) {
		handleRunPython(w, r, root)
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

const maxPythonOutput = 512 << 10 // 512 KiB per stream

type runPythonReq struct {
	Code   string `json:"code"`
	CwdRel string `json:"cwd,omitempty"` // optional directory under workspace (forward slashes ok)
}

type runPythonResp struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
	Error    string `json:"error,omitempty"`
}

func handleRunPython(w http.ResponseWriter, r *http.Request, root string) {
	var req runPythonReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	code := strings.TrimRight(req.Code, "\n")
	if code == "" {
		http.Error(w, "code is empty", http.StatusBadRequest)
		return
	}
	cwdRel := strings.TrimSpace(req.CwdRel)
	cwdRel = filepath.ToSlash(filepath.Clean(cwdRel))
	cwdRel = strings.TrimPrefix(cwdRel, "/")
	var workDir string
	if cwdRel == "" || cwdRel == "." {
		workDir = root
	} else {
		full, err := safeRel(root, cwdRel)
		if err != nil || !underRoot(root, full) {
			http.Error(w, "bad cwd", http.StatusBadRequest)
			return
		}
		st, err := os.Stat(full)
		if err != nil || !st.IsDir() {
			http.Error(w, "cwd is not a directory", http.StatusBadRequest)
			return
		}
		workDir = full
	}
	pythonExe, err := resolvePythonExecutable()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(runPythonResp{
			Error: err.Error(),
		})
		return
	}
	timeout := 60 * time.Second
	if s := strings.TrimSpace(os.Getenv("PYTHON_RUN_TIMEOUT")); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			timeout = d
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, pythonExe, "-u", "-")
	cmd.Dir = workDir
	cmd.Stdin = strings.NewReader(code + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitWriter{dst: &stdout, max: maxPythonOutput}
	cmd.Stderr = &limitWriter{dst: &stderr, max: maxPythonOutput}
	runErr := cmd.Run()
	resp := runPythonResp{
		Stdout: strings.TrimSuffix(stdout.String(), "\n"),
		Stderr: strings.TrimSuffix(stderr.String(), "\n"),
	}
	if runErr != nil {
		if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			resp.Error = "python run timed out"
			resp.ExitCode = -1
		} else if ee := (*exec.ExitError)(nil); errors.As(runErr, &ee) {
			resp.ExitCode = ee.ExitCode()
		} else {
			resp.Error = runErr.Error()
			resp.ExitCode = -1
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

type limitWriter struct {
	dst *bytes.Buffer
	max int
	n   int
}

func (w *limitWriter) Write(p []byte) (int, error) {
	remaining := w.max - w.n
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		wn, _ := w.dst.Write(p[:remaining])
		w.n += wn
		_, _ = w.dst.WriteString("\n… (output truncated)\n")
		w.n = w.max
		return len(p), nil
	}
	wn, err := w.dst.Write(p)
	w.n += wn
	return len(p), err
}

func resolvePythonExecutable() (string, error) {
	if p := strings.TrimSpace(os.Getenv("PYTHON_BIN")); p != "" {
		return p, nil
	}
	for _, name := range []string{"python3", "python"} {
		path, err := exec.LookPath(name)
		if err == nil {
			return path, nil
		}
	}
	return "", errors.New("python not found on PATH (set PYTHON_BIN or install Python)")
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
	Message     string `json:"message"`
	FilePath    string `json:"filePath,omitempty"`
	FileContent string `json:"fileContent,omitempty"`
}

type fileEdit struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type chatResp struct {
	Reply string     `json:"reply"`
	Edits []fileEdit `json:"edits,omitempty"`
	Error string     `json:"error,omitempty"`
}

type modelChatJSON struct {
	Summary string     `json:"summary"`
	Files   []fileEdit `json:"files"`
}

func handleChat(w http.ResponseWriter, r *http.Request, root string) {
	var req chatReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&req); err != nil {
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
			Error: "Set OPENAI_API_KEY in the container to enable chat. Example: docker compose with env from .env",
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	raw, err := openAIChatStructured(base, key, req)
	if err != nil {
		_ = json.NewEncoder(w).Encode(chatResp{Error: err.Error()})
		return
	}
	summary, edits, perr := parseModelChatOutput(raw)
	if perr != nil {
		summary = strings.TrimSpace(raw)
		edits = nil
	} else if strings.TrimSpace(summary) == "" && len(edits) > 0 {
		summary = "Applied updates to your workspace."
	} else if strings.TrimSpace(summary) == "" && len(edits) == 0 {
		summary = "Done."
	}
	validated := make([]fileEdit, 0, len(edits))
	const maxEdit = 2 << 20 // 2 MiB per file from model
	for _, e := range edits {
		rel := strings.TrimSpace(e.Path)
		rel = filepath.ToSlash(filepath.Clean(rel))
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" || rel == "." {
			continue
		}
		full, err := safeRel(root, rel)
		if err != nil || !underRoot(root, full) {
			continue
		}
		if len(e.Content) > maxEdit {
			continue
		}
		validated = append(validated, fileEdit{Path: rel, Content: e.Content})
	}
	_ = json.NewEncoder(w).Encode(chatResp{Reply: summary, Edits: validated})
}

func chatSystemPrompt() string {
	return `You are a Python development assistant inside a small web IDE. The user message includes their request and may include the currently open file path and its full contents.

You MUST respond with a single JSON object only (no markdown fences), using this exact shape:
{"summary":"Brief explanation shown in chat.","files":[{"path":"relative/path/from/workspace/root.py","content":"complete new file contents as a string"}]}

Rules:
- "files" is an array. Include every workspace file you create or change, each with full file text (not a diff).
- Paths use forward slashes relative to the workspace root (e.g. "hello.py", "src/app.py").
- If you only explain or answer without changing files, use "files": [].
- Prefer idiomatic Python 3; keep code runnable and consistent with the user's request.
- Produce strictly valid JSON: escape embedded quotes and newlines in strings as JSON requires.`
}

func buildChatUserPayload(req chatReq) string {
	var b strings.Builder
	b.WriteString("User request:\n")
	b.WriteString(strings.TrimSpace(req.Message))
	b.WriteString("\n\n")
	if strings.TrimSpace(req.FilePath) != "" {
		b.WriteString("Currently open file path (relative to workspace): ")
		b.WriteString(strings.TrimSpace(req.FilePath))
		b.WriteString("\n\nCurrent file contents:\n```\n")
		b.WriteString(req.FileContent)
		b.WriteString("\n```\n")
	} else {
		b.WriteString("No file is currently open in the editor. You may still add or modify files by path under \"files\".\n")
	}
	return b.String()
}

func parseModelChatOutput(raw string) (summary string, files []fileEdit, err error) {
	raw = strings.TrimSpace(raw)
	raw = stripMarkdownJSONFence(raw)
	var out modelChatJSON
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return "", nil, err
	}
	return strings.TrimSpace(out.Summary), out.Files, nil
}

func stripMarkdownJSONFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	rest := strings.TrimSpace(s[3:])
	if strings.HasPrefix(strings.ToLower(rest), "json") {
		rest = strings.TrimSpace(rest[len("json"):])
	}
	if i := strings.Index(rest, "```"); i >= 0 {
		rest = rest[:i]
	}
	return strings.TrimSpace(rest)
}

type openAIRequest struct {
	Model          string            `json:"model"`
	Messages       []openAIMessage   `json:"messages"`
	Stream         bool              `json:"stream"`
	ResponseFormat *openAIRespFormat `json:"response_format,omitempty"`
	MaxTokens      int               `json:"max_tokens,omitempty"`
}

type openAIRespFormat struct {
	Type string `json:"type"`
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

func openAIChatStructured(baseURL, apiKey string, req chatReq) (string, error) {
	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		model = "gpt-4o-mini"
	}
	msgs := []openAIMessage{
		{Role: "system", Content: chatSystemPrompt()},
		{Role: "user", Content: buildChatUserPayload(req)},
	}
	content, err := completeChat(baseURL, apiKey, model, msgs, true)
	if err != nil {
		return "", err
	}
	return content, nil
}

func completeChat(baseURL, apiKey, model string, messages []openAIMessage, jsonMode bool) (string, error) {
	body, status, err := postChatCompletions(baseURL, apiKey, model, messages, jsonMode)
	if err != nil {
		return "", err
	}
	if jsonMode && status == http.StatusBadRequest && shouldRetryChatWithoutJSON(body) {
		body, status, err = postChatCompletions(baseURL, apiKey, model, messages, false)
		if err != nil {
			return "", err
		}
	}
	if status < 200 || status >= 300 {
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

func shouldRetryChatWithoutJSON(apiBody []byte) bool {
	s := strings.ToLower(string(apiBody))
	return strings.Contains(s, "response_format") || strings.Contains(s, "json_object")
}

func postChatCompletions(baseURL, apiKey, model string, messages []openAIMessage, jsonMode bool) ([]byte, int, error) {
	reqObj := openAIRequest{
		Model:     model,
		Messages:  messages,
		Stream:    false,
		MaxTokens: 8192,
	}
	if jsonMode {
		reqObj.ResponseFormat = &openAIRespFormat{Type: "json_object"}
	}
	payload, err := json.Marshal(reqObj)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimSuffix(baseURL, "/")+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return body, resp.StatusCode, nil
}
