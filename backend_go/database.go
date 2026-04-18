package main

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fernet/fernet-go"
)

func (a *App) initDB() error {
	statements := []string{
		`PRAGMA foreign_keys = ON;`,
		`PRAGMA busy_timeout = 5000;`,
		`CREATE TABLE IF NOT EXISTS setup_state (
			id INTEGER PRIMARY KEY,
			google_client_configured INTEGER NOT NULL DEFAULT 0,
			google_account_connected INTEGER NOT NULL DEFAULT 0,
			google_email TEXT,
			google_name TEXT,
			google_picture TEXT,
			selected_agent_id TEXT,
			llm_provider TEXT,
			llm_model TEXT,
			onboarding_completed INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS secret_records (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			key TEXT NOT NULL UNIQUE,
			value TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS preferences (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS custom_agents (
			agent_id TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			auth_mode TEXT NOT NULL DEFAULT 'api_key',
			label TEXT NOT NULL,
			model TEXT NOT NULL,
			reasoning_mode TEXT NOT NULL,
			verbosity TEXT NOT NULL,
			base_url TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS action_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			action_type TEXT NOT NULL,
			summary TEXT NOT NULL,
			payload TEXT,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS conversations (
			id TEXT PRIMARY KEY,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS conversation_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			conversation_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
		);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_secret_records_key ON secret_records(key);`,
		`CREATE INDEX IF NOT EXISTS idx_conversation_messages_conversation_id_id ON conversation_messages(conversation_id, id);`,
		`CREATE TABLE IF NOT EXISTS user_sessions (
			id TEXT PRIMARY KEY,
			email TEXT NOT NULL,
			name TEXT,
			picture TEXT,
			expires_at TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE INDEX IF NOT EXISTS idx_user_sessions_expires_at ON user_sessions(expires_at);`,
		`CREATE TABLE IF NOT EXISTS email_sync_state (
			id INTEGER PRIMARY KEY,
			mailbox_email TEXT,
			sync_status TEXT NOT NULL DEFAULT 'idle',
			sync_mode TEXT,
			last_sync_reason TEXT,
			last_sync_started_at TEXT,
			last_sync_completed_at TEXT,
			last_successful_sync_at TEXT,
			last_full_sync_completed_at TEXT,
			last_partial_sync_completed_at TEXT,
			sync_cursor_history_id TEXT,
			last_history_id TEXT,
			last_sync_error TEXT,
			synced_window_days INTEGER NOT NULL DEFAULT 30,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS synced_emails (
			message_id TEXT PRIMARY KEY,
			thread_id TEXT NOT NULL,
			history_id TEXT,
			subject TEXT,
			subject_normalized TEXT,
			snippet TEXT,
			from_name TEXT,
			from_email TEXT,
			from_raw TEXT,
			reply_to TEXT,
			to_addresses TEXT,
			cc_addresses TEXT,
			bcc_addresses TEXT,
			delivered_to TEXT,
			date_header TEXT,
			internal_date_unix INTEGER,
			internal_date TEXT,
			size_estimate INTEGER,
			body_text TEXT,
			body_html TEXT,
			body_size_estimate INTEGER,
			attachment_count INTEGER NOT NULL DEFAULT 0,
			attachment_names TEXT,
			attachment_total_size INTEGER NOT NULL DEFAULT 0,
			label_ids TEXT,
			list_unsubscribe TEXT,
			list_unsubscribe_post TEXT,
			list_id TEXT,
			precedence_header TEXT,
			auto_submitted_header TEXT,
			feedback_id TEXT,
			in_reply_to TEXT,
			references_header TEXT,
			is_unread INTEGER NOT NULL DEFAULT 0,
			is_starred INTEGER NOT NULL DEFAULT 0,
			is_important INTEGER NOT NULL DEFAULT 0,
			is_in_inbox INTEGER NOT NULL DEFAULT 0,
			is_in_spam INTEGER NOT NULL DEFAULT 0,
			is_in_trash INTEGER NOT NULL DEFAULT 0,
			has_attachments INTEGER NOT NULL DEFAULT 0,
			is_deleted INTEGER NOT NULL DEFAULT 0,
			deleted_at TEXT,
			sync_updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS synced_email_labels (
			message_id TEXT NOT NULL,
			label_id TEXT NOT NULL,
			PRIMARY KEY (message_id, label_id),
			FOREIGN KEY(message_id) REFERENCES synced_emails(message_id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS synced_email_attachments (
			message_id TEXT NOT NULL,
			filename TEXT NOT NULL,
			mime_type TEXT,
			size_estimate INTEGER,
			attachment_id TEXT,
			PRIMARY KEY (message_id, filename, attachment_id),
			FOREIGN KEY(message_id) REFERENCES synced_emails(message_id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS gmail_labels (
			label_id TEXT PRIMARY KEY,
			label_name TEXT NOT NULL,
			label_type TEXT,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS email_embeddings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			message_id TEXT NOT NULL,
			thread_id TEXT NOT NULL,
			chunk_index INTEGER NOT NULL DEFAULT 0,
			embedding_text TEXT NOT NULL,
			source_fingerprint TEXT,
			embedding_vector TEXT NOT NULL,
			subject TEXT,
			from_email TEXT,
			internal_date_unix INTEGER NOT NULL DEFAULT 0,
			has_attachments INTEGER NOT NULL DEFAULT 0,
			is_in_trash INTEGER NOT NULL DEFAULT 0,
			is_in_spam INTEGER NOT NULL DEFAULT 0,
			embedding_provider TEXT NOT NULL,
			embedding_model TEXT NOT NULL,
			embedding_dimensions INTEGER NOT NULL,
			source_sync_updated_at TEXT,
			indexed_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (message_id, chunk_index),
			FOREIGN KEY(message_id) REFERENCES synced_emails(message_id) ON DELETE CASCADE
		);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_email_embeddings_message_chunk ON email_embeddings(message_id, chunk_index);`,
		`CREATE INDEX IF NOT EXISTS idx_email_embeddings_internal_date_unix ON email_embeddings(internal_date_unix DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_email_embeddings_from_email ON email_embeddings(from_email);`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS email_embedding_index USING vec0(
			embedding_id integer primary key,
			embedding_vector float[768] distance_metric=cosine,
			internal_date_unix integer,
			has_attachments boolean,
			is_in_trash boolean,
			is_in_spam boolean,
			from_email text,
			+message_id text,
			+thread_id text,
			+subject text,
			+embedding_text text
		);`,
		`CREATE VIEW IF NOT EXISTS synced_email_labels_with_names AS
			SELECT
				sel.message_id,
				sel.label_id,
				COALESCE(gl.label_name, sel.label_id) AS label_name,
				gl.label_type
			FROM synced_email_labels sel
			LEFT JOIN gmail_labels gl ON sel.label_id = gl.label_id;`,
		`CREATE INDEX IF NOT EXISTS idx_synced_emails_internal_date_unix ON synced_emails(internal_date_unix DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_synced_emails_thread_id ON synced_emails(thread_id);`,
		`CREATE INDEX IF NOT EXISTS idx_synced_emails_from_email ON synced_emails(from_email);`,
		`CREATE INDEX IF NOT EXISTS idx_synced_emails_subject_normalized ON synced_emails(subject_normalized);`,
		`CREATE INDEX IF NOT EXISTS idx_synced_emails_is_deleted_internal_date_unix ON synced_emails(is_deleted, internal_date_unix DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_synced_email_labels_label_id_message_id ON synced_email_labels(label_id, message_id);`,
		`INSERT OR IGNORE INTO setup_state (id) VALUES (1);`,
		`INSERT OR IGNORE INTO email_sync_state (id) VALUES (1);`,
	}
	for _, statement := range statements {
		if _, err := a.db.Exec(statement); err != nil {
			return err
		}
	}
	if err := a.ensureColumnExists("setup_state", "google_name", "TEXT"); err != nil {
		return err
	}
	if err := a.ensureColumnExists("setup_state", "google_picture", "TEXT"); err != nil {
		return err
	}
	if err := a.ensureColumnExists("custom_agents", "auth_mode", "TEXT NOT NULL DEFAULT 'api_key'"); err != nil {
		return err
	}
	if err := a.ensureColumnExists("email_embeddings", "source_fingerprint", "TEXT"); err != nil {
		return err
	}
	return nil
}

func (a *App) ensureColumnExists(tableName string, columnName string, columnType string) error {
	rows, err := a.db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, tableName))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name string
		var dataType string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if strings.EqualFold(name, columnName) {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = a.db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, tableName, columnName, columnType))
	return err
}

func (a *App) getSetupState() (SetupState, error) {
	row := a.db.QueryRow(`SELECT google_client_configured, google_account_connected, google_email, google_name, google_picture, selected_agent_id, llm_provider, llm_model, onboarding_completed FROM setup_state WHERE id = 1`)
	var state SetupState
	var googleClientConfigured int
	var googleAccountConnected int
	var onboardingCompleted int
	var googleEmail sql.NullString
	var googleName sql.NullString
	var googlePicture sql.NullString
	var selectedAgentID sql.NullString
	var llmProvider sql.NullString
	var llmModel sql.NullString
	if err := row.Scan(&googleClientConfigured, &googleAccountConnected, &googleEmail, &googleName, &googlePicture, &selectedAgentID, &llmProvider, &llmModel, &onboardingCompleted); err != nil {
		return SetupState{}, err
	}
	state.GoogleClientConfigured = googleClientConfigured != 0
	state.GoogleAccountConnected = googleAccountConnected != 0
	state.OnboardingCompleted = onboardingCompleted != 0
	if googleEmail.Valid {
		value := googleEmail.String
		state.GoogleEmail = &value
	}
	if googleName.Valid {
		value := googleName.String
		state.GoogleName = &value
	}
	if googlePicture.Valid {
		value := googlePicture.String
		state.GooglePicture = &value
	}
	if selectedAgentID.Valid {
		value := selectedAgentID.String
		state.SelectedAgentID = &value
	}
	if llmProvider.Valid {
		value := llmProvider.String
		state.LLMProvider = &value
	}
	if llmModel.Valid {
		value := llmModel.String
		state.LLMModel = &value
	}
	return state, nil
}

func (a *App) refreshOnboardingState() (SetupState, bool, error) {
	state, err := a.getSetupState()
	if err != nil {
		return SetupState{}, false, err
	}
	llmConfigured, err := a.isLLMConfigured(state)
	if err != nil {
		return SetupState{}, false, err
	}
	onboardingCompleted := state.GoogleClientConfigured && state.GoogleAccountConnected && llmConfigured
	if onboardingCompleted != state.OnboardingCompleted {
		if _, err := a.db.Exec(`UPDATE setup_state SET onboarding_completed = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1`, boolToInt(onboardingCompleted)); err != nil {
			return SetupState{}, false, err
		}
		state.OnboardingCompleted = onboardingCompleted
	}
	return state, llmConfigured, nil
}

func (a *App) saveGoogleClientCredentials(clientID string, clientSecret string) error {
	if err := a.upsertSecret("google_client_id", clientID); err != nil {
		return err
	}
	if err := a.upsertSecret("google_client_secret", clientSecret); err != nil {
		return err
	}
	for _, key := range []string{"google_token_bundle", "google_oauth_state", "google_oauth_code_verifier"} {
		if err := a.deleteSecret(key); err != nil {
			return err
		}
	}
	if _, err := a.db.Exec(`UPDATE setup_state SET google_client_configured = 1, google_account_connected = 0, google_email = NULL, google_name = NULL, google_picture = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = 1`); err != nil {
		return err
	}
	_, _, err := a.refreshOnboardingState()
	return err
}

func (a *App) saveGoogleProfile(email string, name string, picture string) error {
	_, err := a.db.Exec(
		`UPDATE setup_state SET google_email = ?, google_name = ?, google_picture = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1`,
		nullIfEmpty(email),
		nullIfEmpty(name),
		nullIfEmpty(picture),
	)
	return err
}

func (a *App) saveLLMCredentials(agentID string, apiKey string) error {
	agent, ok := getAgentDefinition(agentID)
	if !ok {
		return fmt.Errorf("Unsupported agent: %s", agentID)
	}
	if !agentUsesAPIKey(agent) {
		return errors.New("Selected agent uses Google OAuth. Use Gemini setup instead of an API key.")
	}
	if err := a.upsertSecret("llm_api_key", apiKey); err != nil {
		return err
	}
	return a.saveSelectedAgent(agent)
}

func (a *App) saveGeminiSelection(agentID string) error {
	agent, ok := getAgentDefinition(agentID)
	if !ok {
		return fmt.Errorf("Unsupported agent: %s", agentID)
	}
	if !agentUsesGoogleOAuth(agent) {
		return errors.New("Selected agent is not configured for Google OAuth")
	}
	return a.saveSelectedAgent(agent)
}

func (a *App) saveSelectedAgent(agent AgentDefinition) error {
	previousState, err := a.getSetupState()
	if err != nil {
		return err
	}
	previousEmbeddingConfig := emailEmbeddingConfig{}
	previousEmbeddingSupported := false
	if previousState.SelectedAgentID != nil && strings.TrimSpace(*previousState.SelectedAgentID) != "" {
		if previousAgent, ok := getAgentDefinition(strings.TrimSpace(*previousState.SelectedAgentID)); ok {
			previousEmbeddingConfig, previousEmbeddingSupported = resolveEmailEmbeddingConfig(previousAgent)
		}
	}
	if _, err := a.db.Exec(`UPDATE setup_state SET selected_agent_id = ?, llm_provider = ?, llm_model = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1`, agent.AgentID, agent.Provider, agent.Model); err != nil {
		return err
	}
	_, _, err = a.refreshOnboardingState()
	if err != nil {
		return err
	}

	nextEmbeddingConfig, nextEmbeddingSupported := resolveEmailEmbeddingConfig(agent)
	if previousEmbeddingSupported != nextEmbeddingSupported ||
		previousEmbeddingConfig.Provider != nextEmbeddingConfig.Provider ||
		previousEmbeddingConfig.Model != nextEmbeddingConfig.Model ||
		previousEmbeddingConfig.Dimensions != nextEmbeddingConfig.Dimensions {
		a.refreshEmailEmbeddingsInBackground("selected_agent_changed", true)
	}
	return nil
}

func (a *App) isLLMConfigured(state SetupState) (bool, error) {
	if state.SelectedAgentID == nil || strings.TrimSpace(*state.SelectedAgentID) == "" {
		return false, nil
	}
	agent, ok := getAgentDefinition(*state.SelectedAgentID)
	if !ok {
		return false, nil
	}
	if agentUsesGoogleOAuth(agent) {
		return state.GoogleAccountConnected, nil
	}
	return a.hasSecret("llm_api_key")
}

func (a *App) getCustomAgents() ([]AgentDefinition, error) {
	rows, err := a.db.Query(`SELECT agent_id, provider, COALESCE(auth_mode, 'api_key'), label, model, reasoning_mode, verbosity, base_url FROM custom_agents ORDER BY created_at, agent_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	agents := make([]AgentDefinition, 0)
	for rows.Next() {
		var agent AgentDefinition
		if err := rows.Scan(&agent.AgentID, &agent.Provider, &agent.AuthMode, &agent.Label, &agent.Model, &agent.ReasoningMode, &agent.Verbosity, &agent.BaseURL); err != nil {
			return nil, err
		}
		agent.AuthMode = normalizeAgentAuthMode(agent.AuthMode, agent.Provider)
		agents = append(agents, agent)
	}
	return agents, rows.Err()
}

func (a *App) saveSettings(selectedAgentID *string, apiKey string, agents []SettingsAgentInput) error {
	if _, err := a.db.Exec(`DELETE FROM custom_agents`); err != nil {
		return err
	}
	for _, item := range agents {
		agentID := strings.TrimSpace(item.AgentID)
		provider := strings.TrimSpace(item.Provider)
		authMode := normalizeAgentAuthMode(strings.TrimSpace(item.AuthMode), provider)
		label := strings.TrimSpace(item.Label)
		model := strings.TrimSpace(item.Model)
		reasoningMode := strings.TrimSpace(item.ReasoningMode)
		verbosity := strings.TrimSpace(item.Verbosity)
		baseURL := strings.TrimSpace(item.BaseURL)
		if agentID == "" || provider == "" || label == "" || model == "" || baseURL == "" {
			continue
		}
		if reasoningMode == "" {
			reasoningMode = "standard"
		}
		if verbosity == "" {
			verbosity = "medium"
		}
		if _, err := a.db.Exec(`INSERT INTO custom_agents (agent_id, provider, auth_mode, label, model, reasoning_mode, verbosity, base_url) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, agentID, provider, authMode, label, model, reasoningMode, verbosity, baseURL); err != nil {
			return err
		}
	}
	trimmedAPIKey := strings.TrimSpace(apiKey)
	selected := ""
	if selectedAgentID != nil {
		selected = strings.TrimSpace(*selectedAgentID)
	}
	selectedAgent, hasSelectedAgent := getAgentDefinition(selected)
	shouldStoreAPIKey := trimmedAPIKey != "" && (!hasSelectedAgent || agentUsesAPIKey(selectedAgent))
	if shouldStoreAPIKey {
		if err := a.upsertSecret("llm_api_key", trimmedAPIKey); err != nil {
			return err
		}
	}
	if selected == "" {
		state, err := a.getSetupState()
		if err != nil {
			return err
		}
		if state.SelectedAgentID != nil {
			selected = strings.TrimSpace(*state.SelectedAgentID)
		}
	}
	if selected != "" {
		agent, ok := getAgentDefinition(selected)
		if !ok {
			return fmt.Errorf("Unsupported agent: %s", selected)
		}
		if err := a.saveSelectedAgent(agent); err != nil {
			return err
		}
	}
	_, _, err := a.refreshOnboardingState()
	return err
}

func (a *App) getAllPreferences() ([]PreferenceItem, error) {
	rows, err := a.db.Query(`SELECT id, title, content, created_at, updated_at FROM preferences ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]PreferenceItem, 0)
	for rows.Next() {
		var item PreferenceItem
		var title sql.NullString
		if err := rows.Scan(&item.ID, &title, &item.Value, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.Label = title.String
		items = append(items, item)
	}
	return items, rows.Err()
}

func (a *App) logActionHistory(actionType string, summary string, payload string) error {
	actionType = normalizeHistoryActionType(actionType)
	if actionType == "" {
		return nil
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		summary = "Action executed"
	}
	trimmedSummary := truncateString(summary, 200)

	var payloadValue any
	if strings.TrimSpace(payload) == "" {
		payloadValue = nil
	} else {
		payloadValue = payload
	}

	_, err := a.db.Exec(`INSERT INTO action_history (action_type, summary, payload) VALUES (?, ?, ?)`, actionType, trimmedSummary, payloadValue)
	return err
}

func normalizeHistoryActionType(actionType string) string {
	value := strings.ToLower(strings.TrimSpace(actionType))
	if value == "" {
		return ""
	}
	switch {
	case strings.Contains(value, "delete"):
		return "delete"
	case strings.Contains(value, "create"):
		return "create"
	case strings.Contains(value, "update"), strings.Contains(value, "write"), strings.Contains(value, "modify"), strings.Contains(value, "move"):
		return "update"
	default:
		return ""
	}
}

func (a *App) savePreferences(preferences []PreferenceInput) error {
	existingRows, err := a.db.Query(`SELECT id FROM preferences`)
	if err != nil {
		return err
	}
	defer existingRows.Close()
	existingIDs := make(map[int64]struct{})
	for existingRows.Next() {
		var id int64
		if err := existingRows.Scan(&id); err != nil {
			return err
		}
		existingIDs[id] = struct{}{}
	}
	if err := existingRows.Err(); err != nil {
		return err
	}
	incomingIDs := make(map[int64]struct{})
	for _, pref := range preferences {
		if pref.ID != nil && *pref.ID != 0 {
			incomingIDs[*pref.ID] = struct{}{}
		}
	}
	for id := range existingIDs {
		if _, ok := incomingIDs[id]; ok {
			continue
		}
		if _, err := a.db.Exec(`DELETE FROM preferences WHERE id = ?`, id); err != nil {
			return err
		}
	}
	for _, pref := range preferences {
		value := strings.TrimSpace(pref.Value)
		if value == "" {
			continue
		}
		title := truncateString(value, 100)
		if pref.ID != nil {
			if _, ok := existingIDs[*pref.ID]; ok {
				if _, err := a.db.Exec(`UPDATE preferences SET title = ?, content = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, title, value, *pref.ID); err != nil {
					return err
				}
				continue
			}
		}
		if _, err := a.db.Exec(`INSERT INTO preferences (title, content) VALUES (?, ?)`, title, value); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) getActionHistory(limit int) ([]HistoryItem, error) {
	rows, err := a.db.Query(`SELECT id, action_type, summary, created_at FROM action_history WHERE action_type IN ('create', 'update', 'delete') ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]HistoryItem, 0)
	for rows.Next() {
		var item HistoryItem
		if err := rows.Scan(&item.ID, &item.Action, &item.Details, &item.Timestamp); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (a *App) resolveConversationID(raw *string) (string, error) {
	conversationID := ""
	if raw != nil {
		conversationID = strings.TrimSpace(*raw)
	}
	if conversationID == "" {
		generated, err := generateUUID()
		if err != nil {
			return "", err
		}
		return generated, nil
	}
	if !isValidConversationID(conversationID) {
		return "", errors.New("conversation_id is invalid")
	}
	return conversationID, nil
}

func isValidConversationID(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

func generateUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func (a *App) ensureConversation(conversationID string) error {
	_, err := a.db.Exec(
		`INSERT INTO conversations (id) VALUES (?) ON CONFLICT(id) DO UPDATE SET updated_at = CURRENT_TIMESTAMP`,
		conversationID,
	)
	return err
}

func (a *App) conversationExists(conversationID string) (bool, error) {
	row := a.db.QueryRow(`SELECT 1 FROM conversations WHERE id = ? LIMIT 1`, conversationID)
	var found int
	if err := row.Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (a *App) saveConversationMessage(conversationID string, role string, content string) error {
	if _, err := a.db.Exec(`INSERT INTO conversation_messages (conversation_id, role, content) VALUES (?, ?, ?)`, conversationID, role, content); err != nil {
		return err
	}
	_, err := a.db.Exec(`UPDATE conversations SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, conversationID)
	return err
}

func (a *App) getConversationSummaries(limit int, offset int) ([]ConversationSummary, error) {
	rows, err := a.db.Query(`
		SELECT
			c.id,
			COALESCE(
				NULLIF(TRIM((
					SELECT substr(cm.content, 1, 100)
					FROM conversation_messages cm
					WHERE cm.conversation_id = c.id AND cm.role = 'user'
					ORDER BY cm.id
					LIMIT 1
				)), ''),
				'New chat'
			),
			c.updated_at
		FROM conversations c
		ORDER BY c.updated_at DESC, c.id DESC
		LIMIT ? OFFSET ?
	`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ConversationSummary, 0)
	for rows.Next() {
		var item ConversationSummary
		if err := rows.Scan(&item.ID, &item.Title, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (a *App) deleteConversation(conversationID string) error {
	_, err := a.db.Exec(`DELETE FROM conversations WHERE id = ?`, conversationID)
	return err
}

func (a *App) getConversationMessages(conversationID string) ([]ConversationMessage, error) {
	rows, err := a.db.Query(`SELECT id, conversation_id, role, content, created_at FROM conversation_messages WHERE conversation_id = ? ORDER BY id`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := make([]ConversationMessage, 0)
	for rows.Next() {
		var message ConversationMessage
		if err := rows.Scan(&message.ID, &message.ConversationID, &message.Role, &message.Content, &message.CreatedAt); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func (a *App) hasSecret(key string) (bool, error) {
	row := a.db.QueryRow(`SELECT 1 FROM secret_records WHERE key = ? LIMIT 1`, key)
	var found int
	if err := row.Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (a *App) getSecret(key string) (string, error) {
	row := a.db.QueryRow(`SELECT value FROM secret_records WHERE key = ?`, key)
	var encrypted string
	if err := row.Scan(&encrypted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return a.decryptSecret(encrypted)
}

func (a *App) upsertSecret(key string, value string) error {
	encrypted, err := a.encryptSecret(value)
	if err != nil {
		return err
	}
	_, err = a.db.Exec(`INSERT INTO secret_records (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`, key, encrypted)
	return err
}

func (a *App) deleteSecret(key string) error {
	_, err := a.db.Exec(`DELETE FROM secret_records WHERE key = ?`, key)
	return err
}

func (a *App) encryptSecret(value string) (string, error) {
	key, err := a.loadOrCreateMasterKey()
	if err != nil {
		return "", err
	}
	token, err := fernet.EncryptAndSign([]byte(value), key)
	if err != nil {
		return "", err
	}
	return string(token), nil
}

func (a *App) decryptSecret(value string) (string, error) {
	key, err := a.loadOrCreateMasterKey()
	if err != nil {
		return "", err
	}
	decrypted := fernet.VerifyAndDecrypt([]byte(value), 0, []*fernet.Key{key})
	if decrypted == nil {
		return "", errors.New("failed to decrypt stored secret")
	}
	return string(decrypted), nil
}

func (a *App) loadOrCreateMasterKey() (*fernet.Key, error) {
	masterKeyPath := a.masterKeyPath()
	if err := os.MkdirAll(filepath.Dir(masterKeyPath), 0o700); err != nil {
		return nil, err
	}
	if data, err := os.ReadFile(masterKeyPath); err == nil {
		return fernet.DecodeKey(strings.TrimSpace(string(data)))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := &fernet.Key{}
	key.Generate()
	encoded := key.Encode()
	if err := os.WriteFile(masterKeyPath, []byte(encoded), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func (a *App) masterKeyPath() string {
	return filepath.Join(a.config.DataDir, "secrets", "master.key")
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func truncateString(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func (a *App) createSession(email string, name string, picture string) (string, error) {
	sessionID, err := generateUUID()
	if err != nil {
		return "", err
	}
	expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour)
	_, err = a.db.Exec(
		`INSERT INTO user_sessions (id, email, name, picture, expires_at) VALUES (?, ?, ?, ?, ?)`,
		sessionID, email, nullIfEmpty(name), nullIfEmpty(picture), expiresAt.Format(time.RFC3339),
	)
	if err != nil {
		return "", err
	}
	return sessionID, nil
}

func (a *App) getValidSession(sessionID string) (*UserSession, error) {
	if sessionID == "" {
		return nil, nil
	}
	row := a.db.QueryRow(
		`SELECT id, email, name, picture, expires_at FROM user_sessions WHERE id = ? AND expires_at > ?`,
		sessionID, time.Now().UTC().Format(time.RFC3339),
	)
	var session UserSession
	var name, picture sql.NullString
	var expiresAt string
	if err := row.Scan(&session.ID, &session.Email, &name, &picture, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if name.Valid {
		session.Name = name.String
	}
	if picture.Valid {
		session.Picture = picture.String
	}
	return &session, nil
}

func (a *App) deleteSession(sessionID string) error {
	_, err := a.db.Exec(`DELETE FROM user_sessions WHERE id = ?`, sessionID)
	return err
}

func (a *App) cleanupExpiredSessions() error {
	_, err := a.db.Exec(`DELETE FROM user_sessions WHERE expires_at <= ?`, time.Now().UTC().Format(time.RFC3339))
	return err
}
