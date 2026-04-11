package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"cursorlite/internal/openai"
	"cursorlite/internal/prompts"
	"cursorlite/internal/python"
	"cursorlite/internal/pywalk"
	"cursorlite/internal/undo"
)

type AttachedFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type CodeReq struct {
	Message       string          `json:"message"`
	FilePath      string          `json:"filePath,omitempty"`
	FileContent   string          `json:"fileContent,omitempty"`
	AttachedFiles []AttachedFile  `json:"attachedFiles,omitempty"`
	Cwd           string          `json:"cwd,omitempty"`
	MaxSteps      int             `json:"maxSteps,omitempty"`
	EditorLine    int             `json:"editorLine,omitempty"`
	SelectionStartLine int        `json:"selectionStartLine,omitempty"`
	SelectionEndLine   int        `json:"selectionEndLine,omitempty"`
	HasSelection  bool            `json:"hasSelection,omitempty"`
	SelectionText string          `json:"selectionText,omitempty"`
	LineAtCursor  string          `json:"lineAtCursor,omitempty"`
}

type StepOut struct {
	Rationale string        `json:"rationale,omitempty"`
	Python    string        `json:"python"`
	Run       python.Output `json:"run"`
}

type CodeResp struct {
	Summary string    `json:"summary"`
	Steps   []StepOut `json:"steps"`
	Error   string    `json:"error,omitempty"`
}

type turnJSON struct {
	Rationale string `json:"rationale"`
	Python    string `json:"python"`
	Done      bool   `json:"done"`
	Summary   string `json:"summary"`
}

func systemPrompt() string {
	return `You control a Python 3 workspace via executable code (CodeAct). Each turn you output ONE JSON object only (no markdown fences).

Schema:
{"rationale":"Brief plan for this step.","python":"Python source executed as a script in the workspace cwd (not stdin).","done":false}
When finished:
{"rationale":"Why you are done.","python":"","done":true,"summary":"User-facing summary of what was accomplished."}

Rules:
- Prefer the Python standard library (pathlib, json, os, re, etc.). Code runs with cwd = workspace root or the "cwd" the user specified. Use forward slashes in pathlib strings or Path("a","b.py").
- **Read before you write on existing paths.** For any file that already exists in the workspace (especially .py modules listed in the user message or the open file path), you MUST load the current contents from disk in Python first, e.g. p = pathlib.Path("relative/path.py"); text = p.read_text(encoding="utf-8"), then apply edits in memory (regex, string replace, AST, or line-based patch), then p.write_text(new_text, encoding="utf-8", newline="\n"). Do not replace an entire existing project file with a fresh script you invented from scratch unless the user asked to replace everything.
- If the user has a file open in the IDE, treat that path as the primary target when the goal is about that code: read that path from disk (authoritative), merge your changes, write back to the same path.
- The user payload may include **cursor line** and/or **selected text** in the open file (similar to @-line context in Cursor). Use that to interpret “this line”, “here”, “the code I sent”, or small pasted fragments that match the buffer.
- Greenfield deliverables: you may create new .py files when nothing suitable exists. Otherwise extend or fix what is already there.
- You may read/write files under the workspace, print diagnostics, and use stderr.
- After each of your Python runs, the server sends you stdout, stderr, and exit code. Use that feedback to fix errors or continue.
- Keep each script focused. Compose multiple steps across turns using loops in conversation, not necessarily inside one script.
- Output strictly valid JSON. Escape strings properly.
- Whenever you change code that can be tested, run tests in your Python steps (e.g. subprocess.run([sys.executable, "-m", "pytest", "-q", "--tb=short"], check=False) or unittest) and iterate until they pass before attempting to finish.
- Set "done": true only when the user's goal is met AND automated verification is ready to pass, or you cannot proceed. The server will run python -m pytest -q --tb=short when you set "done": true; if that fails, you must keep fixing until it passes (unless the goal is non-code or there is no pytest in the environment).
- When your goal implied shipping code files, the server checks that some .py file in the workspace actually changed before it accepts "done": true.`
}

func buildUserPayload(req CodeReq, root string) string {
	var b strings.Builder
	b.WriteString("User goal:\n")
	b.WriteString(strings.TrimSpace(req.Message))
	b.WriteString("\n\n")

	paths := pywalk.PythonRelPaths(root, 150)
	if len(paths) > 0 {
		b.WriteString("Existing .py files under the workspace (edit these with read_text → modify → write_text when relevant; list may be truncated):\n")
		for _, p := range paths {
			b.WriteString("- ")
			b.WriteString(p)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if len(req.AttachedFiles) > 0 {
		b.WriteString("Files attached via drag-and-drop (may be outside the workspace). Use their contents for analysis; to save into the workspace, write with pathlib.\n\n")
		for _, af := range req.AttachedFiles {
			name := strings.TrimSpace(af.Name)
			if name == "" {
				name = "(unnamed)"
			}
			b.WriteString("### ")
			b.WriteString(name)
			b.WriteString("\n```\n")
			b.WriteString(af.Content)
			b.WriteString("\n```\n\n")
		}
	}

	if strings.TrimSpace(req.FilePath) != "" {
		fp := strings.TrimSpace(req.FilePath)
		b.WriteString("Currently open in the IDE (workspace-relative path): ")
		b.WriteString(fp)
		b.WriteString("\n\nThe buffer below may be stale vs disk. In your first edit step, read that path from disk with pathlib.Path(...).read_text(encoding=\"utf-8\"), apply changes, then write_text(..., encoding=\"utf-8\") to the same path.\n\nCurrent editor buffer (reference only):\n```\n")
		b.WriteString(req.FileContent)
		b.WriteString("\n```\n")

		if req.EditorLine > 0 || (req.HasSelection && strings.TrimSpace(req.SelectionText) != "") {
			b.WriteString("\n**Where the user was in the editor** (use this to tie their message to a concrete place in `")
			b.WriteString(fp)
			b.WriteString("`):\n")
			if req.HasSelection && strings.TrimSpace(req.SelectionText) != "" {
				sl, el := req.SelectionStartLine, req.SelectionEndLine
				if sl <= 0 {
					sl = req.EditorLine
				}
				if el <= 0 {
					el = sl
				}
				b.WriteString(fmt.Sprintf("- Selected region: lines %d–%d\n", sl, el))
				b.WriteString("- Selected text:\n```\n")
				b.WriteString(req.SelectionText)
				b.WriteString("\n```\n")
			} else if req.EditorLine > 0 {
				b.WriteString(fmt.Sprintf("- Cursor on line %d (no text selected)\n", req.EditorLine))
				if strings.TrimSpace(req.LineAtCursor) != "" {
					b.WriteString("- That line in the buffer:\n```\n")
					b.WriteString(req.LineAtCursor)
					b.WriteString("\n```\n")
				}
			}
		}
	} else {
		b.WriteString("No file is open in the editor.\n")
	}
	b.WriteString("\nIf the goal is to build implementable code (game, app, script, etc.), write or update at least one .py file under the workspace so it appears in the file tree.\n")
	return b.String()
}

func parseTurn(raw string) (turnJSON, error) {
	raw = strings.TrimSpace(raw)
	raw = openai.StripMarkdownJSONFence(raw)
	var out turnJSON
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return turnJSON{}, err
	}
	return out, nil
}

func goalImpliesPythonDeliverable(msg string) bool {
	m := strings.ToLower(strings.TrimSpace(msg))
	if m == "" {
		return false
	}
	verbs := []string{"create", "build", "make", "implement", "write", "develop", "add", "code", "update", "modify", "fix", "edit", "change", "refactor"}
	hasVerb := false
	for _, v := range verbs {
		if strings.Contains(m, v) {
			hasVerb = true
			break
		}
	}
	if !hasVerb {
		return false
	}
	hints := []string{
		"game", "app", "script", "program", "tool", "library", "module",
		"api", "server", "cli", "project", "website", "bot",
		"tic", "tac", "toe", "tictactoe",
	}
	for _, h := range hints {
		if strings.Contains(m, h) {
			return true
		}
	}
	return false
}

func formatFileDeliverableRejection() string {
	return `You set "done": true but no .py file under the workspace changed since this request started.

For implementable deliverables you must change real files on disk: read existing paths with pathlib.Path(...).read_text(encoding="utf-8"), apply edits, then write_text(..., encoding="utf-8") — or create a new .py if appropriate — then set "done": true after verifying.`
}

func verifyEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("AGENT_VERIFY")))
	return v != "0" && v != "false" && v != "off" && v != "no"
}

func isPytestMissing(resp python.Output) bool {
	combined := strings.ToLower(resp.Stdout + "\n" + resp.Stderr + "\n" + resp.Error)
	return strings.Contains(combined, "no module named pytest") ||
		strings.Contains(combined, "unknown command: pytest")
}

func runAutomatedTestGate(ctx context.Context, root, cwdRel string) (resp python.Output, ran bool, ok bool) {
	if !verifyEnabled() {
		return python.Output{}, false, true
	}
	resp = python.RunModule(ctx, root, cwdRel, "pytest", "-q", "--tb=short")
	if isPytestMissing(resp) {
		return python.Output{}, false, true
	}
	if resp.ExitCode == 0 || resp.ExitCode == 5 {
		return resp, true, true
	}
	return resp, true, false
}

func formatVerificationRejection(resp python.Output) string {
	var b strings.Builder
	b.WriteString("You set \"done\": true but automated verification did not pass.\n")
	b.WriteString(`The server ran: ` + "`python -m pytest -q --tb=short`" + ` in the workspace.\n`)
	b.WriteString("Fix the code, use earlier steps to run the same command until it succeeds, then set \"done\": true again.\n\n")
	b.WriteString(formatExecutionFeedback(resp))
	return b.String()
}

func formatExecutionFeedback(resp python.Output) string {
	var b strings.Builder
	b.WriteString("Execution result:\n")
	b.WriteString(fmt.Sprintf("exit_code: %d\n", resp.ExitCode))
	if resp.Error != "" {
		b.WriteString("runtime_error: ")
		b.WriteString(resp.Error)
		b.WriteString("\n")
	}
	if resp.Stdout != "" {
		b.WriteString("stdout:\n")
		b.WriteString(resp.Stdout)
		b.WriteString("\n")
	}
	if resp.Stderr != "" {
		b.WriteString("stderr:\n")
		b.WriteString(resp.Stderr)
		b.WriteString("\n")
	}
	return b.String()
}

func HandleAgentCode(w http.ResponseWriter, r *http.Request, root string, db *sql.DB) {
	var req CodeReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		http.Error(w, "message is required", http.StatusBadRequest)
		return
	}

	if err := undo.SaveSnapshot(root); err != nil {
		log.Printf("agent undo snapshot: %v", err)
	}

	userPayload := buildUserPayload(req, root)
	sysPrompt := systemPrompt()
	model := openai.ResolveModel()
	maxS := maxSteps(req.MaxSteps)

	var msgs []openai.Message
	var steps []StepOut
	var summary string
	var errStr string

	defer func() {
		if db == nil {
			return
		}
		var msgJSON []byte
		if len(msgs) > 0 {
			msgJSON, _ = json.Marshal(msgs)
		}
		stepJSON, _ := json.Marshal(steps)
		if err := prompts.InsertAgentPrompt(r.Context(), db, prompts.InsertParams{
			Message:       req.Message,
			UserPayload:   userPayload,
			SystemPrompt:  sysPrompt,
			FilePath:      req.FilePath,
			Cwd:           req.Cwd,
			Model:         model,
			MaxSteps:      maxS,
			Summary:       summary,
			ErrStr:        errStr,
			MessagesJSON:  msgJSON,
			StepsJSON:     stepJSON,
			AttachedCount: len(req.AttachedFiles),
		}); err != nil {
			log.Printf("prompts db: %v", err)
		}
	}()

	key := os.Getenv("OPENAI_API_KEY")
	base := os.Getenv("OPENAI_BASE_URL")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	w.Header().Set("Content-Type", "application/json")
	if key == "" {
		errStr = "Set OPENAI_API_KEY to use the CodeAct agent."
		msgs = []openai.Message{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: userPayload},
		}
		_ = json.NewEncoder(w).Encode(CodeResp{
			Error: errStr,
		})
		return
	}

	msgs = []openai.Message{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: userPayload},
	}
	pyTimeout := python.RunTimeout()
	overallDeadline := time.Now().Add(time.Duration(maxS+2) * pyTimeout)
	if dl, ok := r.Context().Deadline(); ok && dl.Before(overallDeadline) {
		overallDeadline = dl
	}

	beforePy, _ := pywalk.PythonFingerprints(root)
	if beforePy == nil {
		beforePy = map[string]int64{}
	}

	for range maxS {
		ctxCall, cancelCall := context.WithDeadline(r.Context(), overallDeadline)
		raw, err := completeChatInContext(ctxCall, base, key, model, msgs, true)
		cancelCall()
		if err != nil {
			errStr = err.Error()
			_ = json.NewEncoder(w).Encode(CodeResp{Summary: summary, Steps: steps, Error: errStr})
			return
		}

		turn, err := parseTurn(raw)
		if err != nil {
			msgs = append(msgs, openai.Message{Role: "assistant", Content: raw})
			msgs = append(msgs, openai.Message{Role: "user", Content: "Your last message was not valid JSON matching the schema. Reply with a single JSON object only (rationale, python, done, summary when done)."})
			continue
		}

		if turn.Done {
			candidate := strings.TrimSpace(turn.Summary)
			if candidate == "" {
				candidate = strings.TrimSpace(turn.Rationale)
			}

			ctxVerify, cancelVerify := context.WithDeadline(r.Context(), overallDeadline)
			vResp, vRan, vOK := runAutomatedTestGate(ctxVerify, root, req.Cwd)
			cancelVerify()

			if vRan {
				steps = append(steps, StepOut{
					Rationale: "Server: automated verification before completing",
					Python:    "python -m pytest -q --tb=short",
					Run:       vResp,
				})
			}

			if vRan && !vOK {
				msgs = append(msgs, openai.Message{Role: "assistant", Content: raw})
				msgs = append(msgs, openai.Message{Role: "user", Content: formatVerificationRejection(vResp)})
				continue
			}

			if goalImpliesPythonDeliverable(req.Message) {
				afterPy, err := pywalk.PythonFingerprints(root)
				if err != nil {
					afterPy = map[string]int64{}
				}
				if !pywalk.PythonWorkspaceChanged(beforePy, afterPy) {
					msgs = append(msgs, openai.Message{Role: "assistant", Content: raw})
					msgs = append(msgs, openai.Message{Role: "user", Content: formatFileDeliverableRejection()})
					continue
				}
			}

			summary = candidate
			break
		}

		code := strings.TrimSpace(turn.Python)
		if code == "" {
			msgs = append(msgs, openai.Message{Role: "assistant", Content: raw})
			msgs = append(msgs, openai.Message{Role: "user", Content: `You set "done": false but "python" is empty. Provide Python to run, or set "done": true with a "summary".`})
			continue
		}

		ctxPy, cancelPy := context.WithDeadline(r.Context(), overallDeadline)
		pyCtx, cancelPyTimeout := context.WithTimeout(ctxPy, pyTimeout)
		runResp, runErr := python.RunInWorkspace(pyCtx, root, code, req.Cwd, "")
		cancelPyTimeout()
		cancelPy()
		if runErr != nil {
			runResp = python.Output{Error: runErr.Error(), ExitCode: -1}
		}

		steps = append(steps, StepOut{
			Rationale: turn.Rationale,
			Python:    code,
			Run:       runResp,
		})

		msgs = append(msgs, openai.Message{Role: "assistant", Content: raw})
		msgs = append(msgs, openai.Message{Role: "user", Content: formatExecutionFeedback(runResp)})
	}

	if summary == "" && len(steps) > 0 {
		summary = fmt.Sprintf("Completed %d code step(s). See transcript for details.", len(steps))
	}
	if summary == "" {
		summary = "Agent finished without a summary. Check the transcript or increase max steps."
	}
	_ = json.NewEncoder(w).Encode(CodeResp{Summary: summary, Steps: steps})
}

func completeChatInContext(ctx context.Context, baseURL, apiKey, model string, messages []openai.Message, jsonMode bool) (string, error) {
	type result struct {
		text string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		text, err := openai.CompleteChat(baseURL, apiKey, model, messages, jsonMode)
		ch <- result{text, err}
	}()
	select {
	case <-ctx.Done():
		return "", errors.New(ctx.Err().Error())
	case res := <-ch:
		return res.text, res.err
	}
}
