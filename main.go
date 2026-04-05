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
	"sort"
	"strconv"
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

	promptDB, err := openPromptDB(root)
	if err != nil {
		log.Fatalf("prompts db: %v", err)
	}
	defer promptDB.Close()

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
	mux.HandleFunc("POST /api/run-python", func(w http.ResponseWriter, r *http.Request) {
		handleRunPython(w, r, root)
	})
	mux.HandleFunc("GET /ws/run-python", func(w http.ResponseWriter, r *http.Request) {
		handleRunPythonWS(w, r, root)
	})
	mux.HandleFunc("POST /api/agent-code", func(w http.ResponseWriter, r *http.Request) {
		handleAgentCode(w, r, root, promptDB)
	})
	mux.HandleFunc("GET /api/prompts", func(w http.ResponseWriter, r *http.Request) {
		handlePromptsAPI(w, r, promptDB)
	})
	mux.HandleFunc("GET /api/agent-undo-available", func(w http.ResponseWriter, r *http.Request) {
		handleAgentUndoAvailable(w, r, root)
	})
	mux.HandleFunc("POST /api/agent-undo", func(w http.ResponseWriter, r *http.Request) {
		handleAgentUndo(w, r, root)
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
const maxPythonStdin = 1 << 20      // 1 MiB for Run stdin

const cursorliteInternalDir = ".cursorlite"
const cursorliteRunScript = "run.py"

type runPythonReq struct {
	Code   string `json:"code"`
	CwdRel string `json:"cwd,omitempty"` // optional directory under workspace (forward slashes ok)
	Stdin  string `json:"stdin,omitempty"` // fed to the Python process (e.g. input())
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
	if len(req.Stdin) > maxPythonStdin {
		http.Error(w, "stdin too large", http.StatusRequestEntityTooLarge)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), pythonRunTimeout())
	defer cancel()
	resp, err := runPythonInWorkspace(ctx, root, code, req.CwdRel, req.Stdin)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// resolveWorkspaceWorkDir returns an absolute directory under root for running Python (cwdRel is relative to workspace).
func resolveWorkspaceWorkDir(root, cwdRel string) (string, error) {
	cwdRel = strings.TrimSpace(cwdRel)
	cwdRel = filepath.ToSlash(filepath.Clean(cwdRel))
	cwdRel = strings.TrimPrefix(cwdRel, "/")
	if cwdRel == "" || cwdRel == "." {
		return root, nil
	}
	full, err := safeRel(root, cwdRel)
	if err != nil || !underRoot(root, full) {
		return "", errors.New("bad cwd")
	}
	st, err := os.Stat(full)
	if err != nil || !st.IsDir() {
		return "", errors.New("cwd is not a directory")
	}
	return full, nil
}

func pythonRunTimeout() time.Duration {
	timeout := 60 * time.Second
	if s := strings.TrimSpace(os.Getenv("PYTHON_RUN_TIMEOUT")); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			timeout = d
		}
	}
	return timeout
}

// prepareCursorliteRunScript writes code to workDir/.cursorlite/run.py (hidden from explorer).
func prepareCursorliteRunScript(workDir, code string) (scriptPath string, err error) {
	dir := filepath.Join(workDir, cursorliteInternalDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	scriptPath = filepath.Join(dir, cursorliteRunScript)
	if err := os.WriteFile(scriptPath, []byte(code), 0o644); err != nil {
		return "", err
	}
	return scriptPath, nil
}

// runPythonInWorkspace writes code to a workspace script and runs it so process stdin is free for user input.
// cwdRel is optional, relative to workspace root. stdin is optional data for input().
// Returns a validation error for bad cwd or empty code; "python not found" is reported in runPythonResp.Error with nil err.
func runPythonInWorkspace(ctx context.Context, root, code, cwdRel, stdin string) (runPythonResp, error) {
	code = strings.TrimRight(strings.TrimSpace(code), "\n")
	if code == "" {
		return runPythonResp{}, errors.New("code is empty")
	}
	if len(stdin) > maxPythonStdin {
		return runPythonResp{}, errors.New("stdin too large")
	}
	workDir, err := resolveWorkspaceWorkDir(root, cwdRel)
	if err != nil {
		return runPythonResp{}, err
	}
	scriptPath, err := prepareCursorliteRunScript(workDir, code)
	if err != nil {
		return runPythonResp{}, err
	}
	pythonExe, err := resolvePythonExecutable()
	if err != nil {
		return runPythonResp{Error: err.Error()}, nil
	}
	cmd := exec.CommandContext(ctx, pythonExe, "-u", scriptPath)
	cmd.Dir = workDir
	cmd.Stdin = strings.NewReader(stdin)
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
	return resp, nil
}

// runPythonModuleInWorkspace runs `python -m <module> [args...]` with cwd under the workspace (same limits as stdin runs).
func runPythonModuleInWorkspace(ctx context.Context, root, cwdRel, module string, args ...string) runPythonResp {
	module = strings.TrimSpace(module)
	if module == "" {
		return runPythonResp{Error: "module is empty", ExitCode: -1}
	}
	workDir, err := resolveWorkspaceWorkDir(root, cwdRel)
	if err != nil {
		return runPythonResp{Error: err.Error(), ExitCode: -1}
	}
	pythonExe, err := resolvePythonExecutable()
	if err != nil {
		return runPythonResp{Error: err.Error()}
	}
	argv := append([]string{"-m", module}, args...)
	cmd := exec.CommandContext(ctx, pythonExe, argv...)
	cmd.Dir = workDir
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
	return resp
}

func agentMaxSteps(requested int) int {
	const hardCap = 15
	defaultSteps := 12
	if v := strings.TrimSpace(os.Getenv("AGENT_MAX_STEPS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			defaultSteps = n
		}
	}
	max := defaultSteps
	if requested > 0 {
		max = requested
	}
	if max > hardCap {
		max = hardCap
	}
	if max < 1 {
		max = 1
	}
	return max
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

var skipPythonWalkDirNames = map[string]struct{}{
	".git": {}, ".venv": {}, "venv": {}, "__pycache__": {}, "node_modules": {},
	".mypy_cache": {}, ".tox": {}, ".cursorlite": {},
}

// errWorkspacePyListCap stops workspacePythonRelPaths once enough paths are collected.
var errWorkspacePyListCap = errors.New("workspace python list cap")

// workspacePythonRelPaths returns sorted workspace-relative paths of .py files for agent context (capped).
func workspacePythonRelPaths(root string, limit int) []string {
	if limit <= 0 {
		limit = 200
	}
	out := make([]string, 0, min(limit, 64))
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if _, skip := skipPythonWalkDirNames[d.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.ToLower(filepath.Ext(path)) != ".py" {
			return nil
		}
		if !underRoot(root, path) {
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

// workspacePythonFingerprints maps workspace-relative paths of .py files to a cheap size+mtime fingerprint.
func workspacePythonFingerprints(root string) (map[string]int64, error) {
	out := make(map[string]int64)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if _, skip := skipPythonWalkDirNames[d.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.ToLower(filepath.Ext(path)) != ".py" {
			return nil
		}
		if !underRoot(root, path) {
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

func pythonWorkspaceChanged(before, after map[string]int64) bool {
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

const defaultOpenAIModel = "gpt-4o-mini"

func resolveOpenAIModel() string {
	if m := strings.TrimSpace(os.Getenv("OPENAI_MODEL")); m != "" {
		return m
	}
	return defaultOpenAIModel
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
