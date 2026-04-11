package prompts

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"

	"cursorlite/internal/meta"
	_ "modernc.org/sqlite"
)

func dbPath(root string) string {
	if p := strings.TrimSpace(os.Getenv("PROMPTS_DB_PATH")); p != "" {
		return p
	}
	return filepath.Join(root, meta.CursorliteInternalDir, "prompts.db")
}

func Open(root string) (*sql.DB, error) {
	path := dbPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
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
