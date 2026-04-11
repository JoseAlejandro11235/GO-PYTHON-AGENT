package prompts

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

type ListRow struct {
	ID          int64  `json:"id"`
	CreatedAt   string `json:"createdAt"`
	UserMessage string `json:"userMessage"`
	Summary     string `json:"summary,omitempty"`
	Error       string `json:"error,omitempty"`
	StepCount   int    `json:"stepCount"`
}

type Detail struct {
	ListRow
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

func HandleAPI(w http.ResponseWriter, r *http.Request, db *sql.DB) {
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
	out := []ListRow{}
	for rows.Next() {
		var id int64
		var created, userMsg, summary, errStr sql.NullString
		var steps sql.NullString
		if err := rows.Scan(&id, &created, &userMsg, &summary, &errStr, &steps); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		stepCount := countStepsJSON(steps.String)
		out = append(out, ListRow{
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
	var steps []json.RawMessage
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
	var row Detail
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
