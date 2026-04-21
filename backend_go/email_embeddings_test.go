package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
)

func newTestApp(t *testing.T) *App {
	t.Helper()

	registerSQLiteConnectionHook()

	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	app := &App{
		config: Config{
			DataDir:          tempDir,
			DatabasePath:     dbPath,
			DatabaseURL:      dbPath,
			APIPrefix:        "/api",
			PublicAPIBaseURL: "http://localhost:8000",
		},
		db:         db,
		httpClient: &http.Client{},
	}
	if err := app.initDB(); err != nil {
		t.Fatalf("init db: %v", err)
	}
	return app
}

func TestRebuildEmailEmbeddingIndexAllowsBlankTextMetadata(t *testing.T) {
	app := newTestApp(t)

	if _, err := app.db.Exec(`
		INSERT INTO synced_emails (
			message_id,
			thread_id,
			sync_updated_at
		) VALUES (?, ?, CURRENT_TIMESTAMP)
	`,
		"message-1",
		"thread-1",
	); err != nil {
		t.Fatalf("insert synced email: %v", err)
	}

	vectorJSONBytes, err := json.Marshal(make([]float32, emailEmbeddingDimensions))
	if err != nil {
		t.Fatalf("marshal vector: %v", err)
	}

	result, err := app.db.Exec(`
		INSERT INTO email_embeddings (
			message_id,
			thread_id,
			chunk_index,
			embedding_text,
			embedding_vector,
			subject,
			from_email,
			internal_date_unix,
			has_attachments,
			is_in_trash,
			is_in_spam,
			embedding_provider,
			embedding_model,
			embedding_dimensions
		) VALUES (?, ?, 0, ?, ?, NULL, NULL, ?, 0, 0, 0, ?, ?, ?)
	`,
		"message-1",
		"thread-1",
		"body",
		string(vectorJSONBytes),
		int64(0),
		"openai",
		"text-embedding-3-small",
		emailEmbeddingDimensions,
	)
	if err != nil {
		t.Fatalf("insert email embedding: %v", err)
	}
	embeddingID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}

	if err := app.rebuildEmailEmbeddingIndex(); err != nil {
		t.Fatalf("rebuild email embedding index: %v", err)
	}

	row := app.db.QueryRow(`
		SELECT from_email, subject
		FROM email_embedding_index
		WHERE embedding_id = ?
	`, embeddingID)

	var fromEmail string
	var subject string
	if err := row.Scan(&fromEmail, &subject); err != nil {
		t.Fatalf("scan rebuilt vec row: %v", err)
	}
	if fromEmail != "" {
		t.Fatalf("expected blank from_email, got %q", fromEmail)
	}
	if subject != "" {
		t.Fatalf("expected blank subject, got %q", subject)
	}
}
