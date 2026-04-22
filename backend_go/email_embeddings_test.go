package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newTestApp(t *testing.T) *App {
	t.Helper()

	registerSQLiteDriver()

	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")
	db, err := sql.Open(sqliteDriverName, dbPath)
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
	app.emailEmbeddingCond = sync.NewCond(&app.emailEmbeddingMu)
	app.emailEmbeddingPendingLimit = defaultEmailEmbeddingQueueSize
	if err := app.initDB(); err != nil {
		t.Fatalf("init db: %v", err)
	}
	return app
}

func skipUnlessIncrementalVecSupported(t *testing.T, app *App) {
	t.Helper()

	var version string
	if err := app.db.QueryRow(`SELECT vec_version()`).Scan(&version); err != nil {
		t.Fatalf("query vec version: %v", err)
	}
	if strings.Contains(version, "v0.1.9") || strings.Contains(version, "v0.1.10") {
		return
	}
	t.Skipf("vec runtime %q does not yet support DELETE and INSERT OR REPLACE safely; enable these tests only with sqlite-vec >= v0.1.9", version)
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

func TestApplyEmailEmbeddingUpsertDeleteMaintainsVecIndex(t *testing.T) {
	app := newTestApp(t)
	skipUnlessIncrementalVecSupported(t, app)

	if _, err := app.db.Exec(`
		INSERT INTO synced_emails (
			message_id,
			thread_id,
			subject,
			from_email,
			body_text,
			internal_date_unix,
			sync_updated_at
		) VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`,
		"message-1",
		"thread-1",
		"Original subject",
		"sender@example.com",
		"Original body",
		int64(111),
	); err != nil {
		t.Fatalf("insert synced email: %v", err)
	}

	source, ok, err := app.getEmailEmbeddingSourceByMessageID("message-1")
	if err != nil {
		t.Fatalf("get embedding source: %v", err)
	}
	if !ok {
		t.Fatal("expected embedding source")
	}
	document := buildEmailEmbeddingDocument(source)
	config := emailEmbeddingConfig{
		Provider:   "openai",
		Model:      "text-embedding-3-small",
		Dimensions: emailEmbeddingDimensions,
	}
	vector := make([]float32, emailEmbeddingDimensions)
	vector[0] = 0.25
	if err := app.applyEmailEmbeddingUpsert(document, vector, config); err != nil {
		t.Fatalf("apply embedding upsert: %v", err)
	}

	var embeddingID int64
	var subject string
	var fromEmail string
	if err := app.db.QueryRow(`
		SELECT ee.id, idx.subject, idx.from_email
		FROM email_embeddings ee
		JOIN email_embedding_index idx ON idx.embedding_id = ee.id
		WHERE ee.message_id = ?
	`,
		"message-1",
	).Scan(&embeddingID, &subject, &fromEmail); err != nil {
		t.Fatalf("query indexed embedding: %v", err)
	}
	if embeddingID == 0 {
		t.Fatal("expected non-zero embedding id")
	}
	if subject != "Original subject" {
		t.Fatalf("expected indexed subject to match source, got %q", subject)
	}
	if fromEmail != "sender@example.com" {
		t.Fatalf("expected indexed from_email to match source, got %q", fromEmail)
	}

	if err := app.deleteEmailEmbeddingByMessageID("message-1"); err != nil {
		t.Fatalf("delete embedding by message id: %v", err)
	}

	var remaining int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM email_embeddings WHERE message_id = ?`, "message-1").Scan(&remaining); err != nil {
		t.Fatalf("count canonical rows: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("expected no canonical rows after delete, got %d", remaining)
	}
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM email_embedding_index WHERE embedding_id = ?`, embeddingID).Scan(&remaining); err != nil {
		t.Fatalf("count vec rows: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("expected no vec rows after delete, got %d", remaining)
	}
}

func TestApplyEmailEmbeddingMetadataRefreshReplacesVecMetadata(t *testing.T) {
	app := newTestApp(t)
	skipUnlessIncrementalVecSupported(t, app)

	if _, err := app.db.Exec(`
		INSERT INTO synced_emails (
			message_id,
			thread_id,
			subject,
			from_email,
			body_text,
			internal_date_unix,
			is_in_trash,
			sync_updated_at
		) VALUES (?, ?, ?, ?, ?, ?, 0, CURRENT_TIMESTAMP)
	`,
		"message-2",
		"thread-2",
		"Same subject",
		"sender@example.com",
		"Same body",
		int64(100),
	); err != nil {
		t.Fatalf("insert synced email: %v", err)
	}

	source, ok, err := app.getEmailEmbeddingSourceByMessageID("message-2")
	if err != nil {
		t.Fatalf("get original source: %v", err)
	}
	if !ok {
		t.Fatal("expected original source")
	}
	document := buildEmailEmbeddingDocument(source)
	config := emailEmbeddingConfig{
		Provider:   "openai",
		Model:      "text-embedding-3-small",
		Dimensions: emailEmbeddingDimensions,
	}
	vector := make([]float32, emailEmbeddingDimensions)
	vector[0] = 0.5
	if err := app.applyEmailEmbeddingUpsert(document, vector, config); err != nil {
		t.Fatalf("apply embedding upsert: %v", err)
	}

	if _, err := app.db.Exec(`
		UPDATE synced_emails
		SET internal_date_unix = ?, is_in_trash = 1, sync_updated_at = CURRENT_TIMESTAMP
		WHERE message_id = ?
	`, int64(200), "message-2"); err != nil {
		t.Fatalf("update synced email metadata: %v", err)
	}

	updatedSource, ok, err := app.getEmailEmbeddingSourceByMessageID("message-2")
	if err != nil {
		t.Fatalf("get updated source: %v", err)
	}
	if !ok {
		t.Fatal("expected updated source")
	}
	updatedDocument := buildEmailEmbeddingDocument(updatedSource)
	if updatedDocument.SourceFingerprint != document.SourceFingerprint {
		t.Fatal("expected metadata-only update to keep fingerprint stable")
	}

	existingState, ok, err := app.getEmailEmbeddingExistingState("message-2")
	if err != nil {
		t.Fatalf("get existing state: %v", err)
	}
	if !ok {
		t.Fatal("expected existing embedding state")
	}
	if !emailEmbeddingMetadataChanged(existingState, updatedDocument) {
		t.Fatal("expected metadata change detection to trigger")
	}
	if err := app.applyEmailEmbeddingMetadataRefresh(updatedDocument, existingState); err != nil {
		t.Fatalf("apply metadata refresh: %v", err)
	}

	var internalDateUnix int64
	var isInTrash int
	if err := app.db.QueryRow(`
		SELECT internal_date_unix, is_in_trash
		FROM email_embedding_index
		WHERE embedding_id = ?
	`, existingState.EmbeddingID).Scan(&internalDateUnix, &isInTrash); err != nil {
		t.Fatalf("query refreshed vec metadata: %v", err)
	}
	if internalDateUnix != 200 {
		t.Fatalf("expected refreshed internal_date_unix=200, got %d", internalDateUnix)
	}
	if isInTrash != 1 {
		t.Fatalf("expected refreshed is_in_trash=1, got %d", isInTrash)
	}
}
