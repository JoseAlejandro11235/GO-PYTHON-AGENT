package prompts

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

type InsertParams struct {
	Message       string
	UserPayload   string
	SystemPrompt  string
	FilePath      string
	Cwd           string
	Model         string
	MaxSteps      int
	Summary       string
	ErrStr        string
	MessagesJSON  []byte
	StepsJSON     []byte
	AttachedCount int
}

func InsertAgentPrompt(ctx context.Context, db *sql.DB, p InsertParams) error {
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
		strings.TrimSpace(p.Message),
		p.UserPayload,
		p.SystemPrompt,
		nullIfEmptyBytes(p.MessagesJSON),
		nullStr(strings.TrimSpace(p.FilePath)),
		nullStr(strings.TrimSpace(p.Cwd)),
		nullStr(strings.TrimSpace(p.Model)),
		p.MaxSteps,
		nullStr(strings.TrimSpace(p.Summary)),
		nullStr(strings.TrimSpace(p.ErrStr)),
		nullIfEmptyBytes(p.StepsJSON),
		p.AttachedCount,
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
