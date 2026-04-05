package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func promptDBPath(root string) string {
	if p := strings.TrimSpace(os.Getenv("PROMPTS_DB_PATH")); p != "" {
		return p
	}
	return filepath.Join(root, cursorliteInternalDir, "prompts.db")
}

func openPromptDB(root string) (*sql.DB, error) {
	path := promptDBPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migratePrompts(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func migratePrompts(db *sql.DB) error {
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS agent_prompts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at TEXT NOT NULL,
  user_message TEXT NOT NULL,
  user_payload TEXT NOT NULL,
  system_prompt TEXT NOT NULL,
  messages_json TEXT,
  file_path TEXT,
  cwd TEXT,
  model TEXT,
  max_steps INTEGER NOT NULL DEFAULT 0,
  summary TEXT,
  error TEXT,
  steps_json TEXT,
  attached_count INTEGER NOT NULL DEFAULT 0
);`); err != nil {
		return err
	}
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_agent_prompts_created ON agent_prompts(created_at DESC);`)
	return err
}

func insertAgentPrompt(ctx context.Context, db *sql.DB, req agentCodeReq, userPayload, sysPrompt, model string, maxSteps int, summary, errStr string, messagesJSON, stepsJSON []byte) error {
	if db == nil {
		return nil
	}
	created := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.ExecContext(ctx, `
INSERT INTO agent_prompts (
  created_at, user_message, user_payload, system_prompt, messages_json,
  file_path, cwd, model, max_steps, summary, error, steps_json, attached_count
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		created,
		strings.TrimSpace(req.Message),
		userPayload,
		sysPrompt,
		nullIfEmptyBytes(messagesJSON),
		nullStr(strings.TrimSpace(req.FilePath)),
		nullStr(strings.TrimSpace(req.Cwd)),
		nullStr(strings.TrimSpace(model)),
		maxSteps,
		nullStr(strings.TrimSpace(summary)),
		nullStr(strings.TrimSpace(errStr)),
		nullIfEmptyBytes(stepsJSON),
		len(req.AttachedFiles),
	)
	return err
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullIfEmptyBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

type promptListRow struct {
	ID          int64  `json:"id"`
	CreatedAt   string `json:"createdAt"`
	UserMessage string `json:"userMessage"`
	Summary     string `json:"summary,omitempty"`
	Error       string `json:"error,omitempty"`
	StepCount   int    `json:"stepCount"`
}

type promptDetail struct {
	promptListRow
	UserPayload   string          `json:"userPayload"`
	SystemPrompt  string          `json:"systemPrompt"`
	FilePath      string          `json:"filePath,omitempty"`
	Cwd           string          `json:"cwd,omitempty"`
	Model         string          `json:"model,omitempty"`
	MaxSteps      int             `json:"maxSteps"`
	MessagesJSON  json.RawMessage `json:"messages,omitempty"`
	StepsJSON     json.RawMessage `json:"steps,omitempty"`
	AttachedCount int             `json:"attachedCount"`
}

func handlePromptsAPI(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	if db == nil {
		http.Error(w, "prompts database unavailable", http.StatusServiceUnavailable)
		return
	}
	idStr := strings.TrimSpace(r.URL.Query().Get("id"))
	if idStr != "" {
		handleGetPrompt(w, r, db, idStr)
		return
	}
	handleListPrompts(w, r, db)
}

func handleListPrompts(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	limit := 50
	if s := strings.TrimSpace(r.URL.Query().Get("limit")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	rows, err := db.Query(`
SELECT id, created_at, user_message, summary, error, steps_json
FROM agent_prompts
ORDER BY id DESC
LIMIT ?`, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := []promptListRow{}
	for rows.Next() {
		var id int64
		var created, userMsg, summary, errStr sql.NullString
		var steps sql.NullString
		if err := rows.Scan(&id, &created, &userMsg, &summary, &errStr, &steps); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		stepCount := countStepsJSON(steps.String)
		out = append(out, promptListRow{
			ID:          id,
			CreatedAt:   created.String,
			UserMessage: userMsg.String,
			Summary:     summary.String,
			Error:       errStr.String,
			StepCount:   stepCount,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func countStepsJSON(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	var steps []agentStepOut
	if err := json.Unmarshal([]byte(s), &steps); err != nil {
		return 0
	}
	return len(steps)
}

func handleGetPrompt(w http.ResponseWriter, r *http.Request, db *sql.DB, idStr string) {
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id < 1 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	var row promptDetail
	var filePath, cwd, model sql.NullString
	var summaryNS, errorNS sql.NullString
	var maxSteps int
	var messagesJSON, stepsJSON sql.NullString
	var attached int
	err = db.QueryRow(`
SELECT id, created_at, user_message, user_payload, system_prompt, messages_json,
       file_path, cwd, model, max_steps, summary, error, steps_json, attached_count
FROM agent_prompts WHERE id = ?`, id).Scan(
		&row.ID,
		&row.CreatedAt,
		&row.UserMessage,
		&row.UserPayload,
		&row.SystemPrompt,
		&messagesJSON,
		&filePath,
		&cwd,
		&model,
		&maxSteps,
		&summaryNS,
		&errorNS,
		&stepsJSON,
		&attached,
	)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	row.FilePath = filePath.String
	row.Cwd = cwd.String
	row.Model = model.String
	row.MaxSteps = maxSteps
	row.Summary = summaryNS.String
	row.Error = errorNS.String
	row.AttachedCount = attached
	if messagesJSON.Valid && messagesJSON.String != "" {
		row.MessagesJSON = json.RawMessage(messagesJSON.String)
	}
	if stepsJSON.Valid && stepsJSON.String != "" {
		row.StepsJSON = json.RawMessage(stepsJSON.String)
	}
	row.StepCount = countStepsJSON(stepsJSON.String)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(row)
}
