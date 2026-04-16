package main

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
		`INSERT OR IGNORE INTO setup_state (id) VALUES (1);`,
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
	llmConfigured, err := a.hasSecret("llm_api_key")
	if err != nil {
		return SetupState{}, false, err
	}
	if state.SelectedAgentID == nil || strings.TrimSpace(*state.SelectedAgentID) == "" {
		llmConfigured = false
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
	if err := a.upsertSecret("llm_api_key", apiKey); err != nil {
		return err
	}
	if _, err := a.db.Exec(`UPDATE setup_state SET selected_agent_id = ?, llm_provider = ?, llm_model = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1`, agent.AgentID, agent.Provider, agent.Model); err != nil {
		return err
	}
	_, _, err := a.refreshOnboardingState()
	return err
}

func (a *App) getCustomAgents() ([]AgentDefinition, error) {
	rows, err := a.db.Query(`SELECT agent_id, provider, label, model, reasoning_mode, verbosity, base_url FROM custom_agents ORDER BY created_at, agent_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	agents := make([]AgentDefinition, 0)
	for rows.Next() {
		var agent AgentDefinition
		if err := rows.Scan(&agent.AgentID, &agent.Provider, &agent.Label, &agent.Model, &agent.ReasoningMode, &agent.Verbosity, &agent.BaseURL); err != nil {
			return nil, err
		}
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
		if _, err := a.db.Exec(`INSERT INTO custom_agents (agent_id, provider, label, model, reasoning_mode, verbosity, base_url) VALUES (?, ?, ?, ?, ?, ?, ?)`, agentID, provider, label, model, reasoningMode, verbosity, baseURL); err != nil {
			return err
		}
	}
	trimmedAPIKey := strings.TrimSpace(apiKey)
	if trimmedAPIKey != "" {
		if err := a.upsertSecret("llm_api_key", trimmedAPIKey); err != nil {
			return err
		}
	}
	selected := ""
	if selectedAgentID != nil {
		selected = strings.TrimSpace(*selectedAgentID)
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
		if _, err := a.db.Exec(`UPDATE setup_state SET selected_agent_id = ?, llm_provider = ?, llm_model = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1`, agent.AgentID, agent.Provider, agent.Model); err != nil {
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
