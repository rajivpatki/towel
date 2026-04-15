package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fernet/fernet-go"
	_ "modernc.org/sqlite"
)

const (
	googleAuthEndpoint     = "https://accounts.google.com/o/oauth2/v2/auth"
	googleUserinfoEndpoint = "https://openidconnect.googleapis.com/v1/userinfo"
	frontendSetupURL       = "http://localhost:3000/setup/gmail"
	maxToolCallIterations  = 20
	streamSessionTTL       = 15 * time.Minute
)

type Config struct {
	AppName          string
	APIPrefix        string
	DatabaseURL      string
	DatabasePath     string
	DataDir          string
	PublicAPIBaseURL string
	CORSOrigins      []string
}

type App struct {
	config     Config
	db         *sql.DB
	httpClient *http.Client

	streamMu         sync.Mutex
	streamSessions   map[string]*streamSession
	nextSubscriberID int64
}

type AgentDefinition struct {
	AgentID       string `json:"agent_id"`
	Provider      string `json:"provider"`
	Label         string `json:"label"`
	Model         string `json:"model"`
	ReasoningMode string `json:"reasoning_mode"`
	Verbosity     string `json:"verbosity"`
	BaseURL       string `json:"-"`
}

type GmailToolDefinition struct {
	Name         string   `json:"name"`
	GmailActions []string `json:"gmail_actions"`
	Description  string   `json:"description"`
	SafetyModel  string   `json:"safety_model"`
}

type SetupState struct {
	GoogleClientConfigured bool
	GoogleAccountConnected bool
	GoogleEmail            *string
	SelectedAgentID        *string
	LLMProvider            *string
	LLMModel               *string
	OnboardingCompleted    bool
}

type SetupStatus struct {
	GoogleClientConfigured bool                  `json:"google_client_configured"`
	GoogleAccountConnected bool                  `json:"google_account_connected"`
	GoogleEmail            *string               `json:"google_email"`
	LLMConfigured          bool                  `json:"llm_configured"`
	SelectedAgentID        *string               `json:"selected_agent_id"`
	OnboardingCompleted    bool                  `json:"onboarding_completed"`
	AvailableAgents        []AgentDefinition     `json:"available_agents"`
	GmailTools             []GmailToolDefinition `json:"gmail_tools"`
}

type SuccessResponse struct {
	Success bool `json:"success"`
}

type GoogleOAuthCredentialsIn struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

type GoogleAuthURLResponse struct {
	AuthURL string `json:"auth_url"`
}

type LLMSetupIn struct {
	AgentID string `json:"agent_id"`
	APIKey  string `json:"api_key"`
}

type ChatMessageIn struct {
	Message        string  `json:"message"`
	ConversationID *string `json:"conversation_id,omitempty"`
}

type ChatMessageOut struct {
	ConversationID string   `json:"conversation_id"`
	Response       string   `json:"response"`
	Actions        []string `json:"actions"`
}

type ChatSessionStartOut struct {
	ConversationID string `json:"conversation_id"`
	SessionID      string `json:"session_id"`
}

type ConversationMessage struct {
	ID             int64  `json:"id"`
	ConversationID string `json:"conversation_id"`
	Role           string `json:"role"`
	Content        string `json:"content"`
	CreatedAt      string `json:"created_at"`
}

type ConversationMessagesOut struct {
	ConversationID string                `json:"conversation_id"`
	Messages       []ConversationMessage `json:"messages"`
}

type HistoryItem struct {
	ID        int64  `json:"id"`
	Action    string `json:"action"`
	Details   string `json:"details"`
	Timestamp string `json:"timestamp"`
}

type HistoryListOut struct {
	Items []HistoryItem `json:"items"`
}

type PreferenceItem struct {
	ID        int64  `json:"id"`
	Label     string `json:"label"`
	Value     string `json:"value"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type PreferencesOut struct {
	Preferences []PreferenceItem `json:"preferences"`
}

type PreferenceInput struct {
	ID    *int64 `json:"id"`
	Value string `json:"value"`
}

type PreferencesIn struct {
	Preferences []PreferenceInput `json:"preferences"`
}

type errorResponse struct {
	Detail string `json:"detail"`
}

type streamEvent struct {
	ID    int64
	Event string
	Data  []byte
}

type streamSession struct {
	ID          string
	Events      []streamEvent
	NextEventID int64
	Subscribers map[int64]chan streamEvent
	Completed   bool
	UpdatedAt   time.Time
}

type llmToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type llmResponseMessage struct {
	Role      string        `json:"role"`
	Content   any           `json:"content"`
	ToolCalls []llmToolCall `json:"tool_calls"`
}

var agentDefinitions = []AgentDefinition{
	{
		AgentID:       "openai:gpt-5.4",
		Provider:      "openai",
		Label:         "OpenAI GPT 5.4",
		Model:         "gpt-5.4",
		ReasoningMode: "thinking",
		Verbosity:     "low",
		BaseURL:       "https://api.openai.com/v1",
	},
	{
		AgentID:       "deepseek:deepseek-thinking",
		Provider:      "deepseek",
		Label:         "DeepSeek Thinking",
		Model:         "deepseek-reasoner",
		ReasoningMode: "thinking",
		Verbosity:     "low",
		BaseURL:       "https://api.deepseek.com/v1",
	},
}

var gmailToolDefinitions = []GmailToolDefinition{
	{
		Name:         "gmail.list_labels",
		GmailActions: []string{"users.labels.list"},
		Description:  "Loads the user's current Gmail labels so Towel can learn the existing taxonomy and avoid mixing with non-Towel labels.",
		SafetyModel:  "read_only",
	},
	{
		Name:         "gmail.create_towel_label",
		GmailActions: []string{"users.labels.create"},
		Description:  "Creates labels strictly under the Towel/ hierarchy, such as Towel/Spam, Towel/Delete, or learned organizational labels.",
		SafetyModel:  "safe_write",
	},
	{
		Name:         "gmail.list_messages",
		GmailActions: []string{"users.messages.list"},
		Description:  "Finds unread, high-volume, or suspicious message sets for analysis and triage.",
		SafetyModel:  "read_only",
	},
	{
		Name:         "gmail.read_message",
		GmailActions: []string{"users.messages.get"},
		Description:  "Reads individual email content and metadata so the agent can classify, summarize, and detect scams or spam patterns.",
		SafetyModel:  "read_only",
	},
	{
		Name:         "gmail.move_to_towel_spam",
		GmailActions: []string{"users.messages.batchModify", "users.labels.create"},
		Description:  "Applies or creates Towel/Spam and removes inbox visibility instead of using Gmail's destructive spam action.",
		SafetyModel:  "pseudo_spam",
	},
	{
		Name:         "gmail.move_to_towel_delete",
		GmailActions: []string{"users.messages.batchModify", "users.labels.create"},
		Description:  "Applies or creates Towel/Delete and removes inbox visibility instead of deleting messages.",
		SafetyModel:  "pseudo_delete",
	},
	{
		Name:         "gmail.create_filter",
		GmailActions: []string{"users.settings.filters.create", "users.labels.create"},
		Description:  "Creates Gmail filters that route future emails into existing or new Towel/ labels based on learned rules and user preferences.",
		SafetyModel:  "safe_write",
	},
	{
		Name:         "gmail.sender_analytics",
		GmailActions: []string{"users.messages.list", "users.messages.get"},
		Description:  "Aggregates sender and domain volume statistics so the UI can recommend cleanup or automation actions.",
		SafetyModel:  "read_only",
	},
}

func main() {
	config := loadConfig()
	app, err := newApp(config)
	if err != nil {
		log.Fatalf("failed to initialize backend: %v", err)
	}
	defer app.db.Close()

	server := &http.Server{
		Addr:              ":8000",
		Handler:           app.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("listening on %s", server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server failed: %v", err)
	}
}

func loadConfig() Config {
	databaseURL := envOrDefault("DATABASE_URL", "sqlite+aiosqlite:////data/towel.db")
	dataDir := envOrDefault("DATA_DIR", "/data")
	apiPrefix := strings.TrimSpace(envOrDefault("API_PREFIX", "/api"))
	if apiPrefix == "" {
		apiPrefix = "/api"
	}
	if !strings.HasPrefix(apiPrefix, "/") {
		apiPrefix = "/" + apiPrefix
	}
	corsRaw := envOrDefault("CORS_ORIGINS", "http://localhost:3000,http://127.0.0.1:3000")
	origins := make([]string, 0)
	for _, origin := range strings.Split(corsRaw, ",") {
		trimmed := strings.TrimSpace(origin)
		if trimmed != "" {
			origins = append(origins, trimmed)
		}
	}
	return Config{
		AppName:          envOrDefault("APP_NAME", "Towel"),
		APIPrefix:        apiPrefix,
		DatabaseURL:      databaseURL,
		DatabasePath:     parseDatabasePath(databaseURL, dataDir),
		DataDir:          dataDir,
		PublicAPIBaseURL: strings.TrimRight(envOrDefault("PUBLIC_API_BASE_URL", "http://localhost:8000"), "/"),
		CORSOrigins:      origins,
	}
}

func envOrDefault(key string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func parseDatabasePath(databaseURL string, dataDir string) string {
	prefixes := []string{"sqlite+aiosqlite:///", "sqlite:///"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(databaseURL, prefix) {
			trimmed := databaseURL[len(prefix):]
			if !strings.HasPrefix(trimmed, "/") && !strings.Contains(trimmed, ":") {
				trimmed = filepath.Join(dataDir, trimmed)
			}
			return filepath.Clean(trimmed)
		}
	}
	if strings.TrimSpace(databaseURL) == "" {
		return filepath.Join(dataDir, "towel.db")
	}
	return filepath.Clean(databaseURL)
}

func newApp(config Config) (*App, error) {
	if err := os.MkdirAll(filepath.Dir(config.DatabasePath), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", config.DatabasePath)
	if err != nil {
		return nil, err
	}
	app := &App{
		config: config,
		db:     db,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
		streamSessions: make(map[string]*streamSession),
	}
	if err := app.initDB(); err != nil {
		db.Close()
		return nil, err
	}
	return app, nil
}

func (a *App) initDB() error {
	statements := []string{
		`PRAGMA foreign_keys = ON;`,
		`PRAGMA busy_timeout = 5000;`,
		`CREATE TABLE IF NOT EXISTS setup_state (
			id INTEGER PRIMARY KEY,
			google_client_configured INTEGER NOT NULL DEFAULT 0,
			google_account_connected INTEGER NOT NULL DEFAULT 0,
			google_email TEXT,
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
	return nil
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.handleRoot)
	mux.HandleFunc("/health", a.handleHealth)
	mux.HandleFunc(a.config.APIPrefix+"/setup/status", a.handleSetupStatus)
	mux.HandleFunc(a.config.APIPrefix+"/setup/google/oauth-credentials", a.handleSaveGoogleCredentials)
	mux.HandleFunc(a.config.APIPrefix+"/setup/google/connect", a.handleGoogleConnect)
	mux.HandleFunc(a.config.APIPrefix+"/setup/google/callback", a.handleGoogleCallback)
	mux.HandleFunc(a.config.APIPrefix+"/setup/llm", a.handleSaveLLMSetup)
	mux.HandleFunc(a.config.APIPrefix+"/tools/gmail", a.handleGmailTools)
	mux.HandleFunc(a.config.APIPrefix+"/chat", a.handleChat)
	mux.HandleFunc(a.config.APIPrefix+"/chat/session", a.handleChatSession)
	mux.HandleFunc(a.config.APIPrefix+"/chat/stream", a.handleChatStream)
	mux.HandleFunc(a.config.APIPrefix+"/chat/conversations/", a.handleConversationMessages)
	mux.HandleFunc(a.config.APIPrefix+"/history", a.handleHistory)
	mux.HandleFunc(a.config.APIPrefix+"/preferences", a.handlePreferences)
	return a.withCORS(mux)
}

func (a *App) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if allowed := a.allowedOrigin(origin); allowed != "" {
			w.Header().Set("Access-Control-Allow-Origin", allowed)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Last-Event-ID")
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) allowedOrigin(origin string) string {
	if origin == "" {
		return ""
	}
	for _, allowed := range a.config.CORSOrigins {
		if origin == allowed {
			return origin
		}
	}
	return ""
}

func (a *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": a.config.AppName, "status": "ok"})
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
}

func (a *App) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	status, err := a.buildSetupStatus()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (a *App) handleSaveGoogleCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var payload GoogleOAuthCredentialsIn
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	payload.ClientID = strings.TrimSpace(payload.ClientID)
	payload.ClientSecret = strings.TrimSpace(payload.ClientSecret)
	if len(payload.ClientID) < 10 || len(payload.ClientSecret) < 10 {
		writeError(w, http.StatusBadRequest, "client_id and client_secret must each be at least 10 characters")
		return
	}
	if err := a.saveGoogleClientCredentials(payload.ClientID, payload.ClientSecret); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
}

func (a *App) handleGoogleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	authURL, err := a.buildGoogleAuthURL()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, GoogleAuthURLResponse{AuthURL: authURL})
}

func (a *App) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if code == "" || state == "" {
		writeHTML(w, http.StatusBadRequest, failureHTML("OAuth callback is missing required query parameters."))
		return
	}
	if err := a.completeGoogleOAuthCallback(code, state); err != nil {
		writeHTML(w, http.StatusBadRequest, failureHTML(err.Error()))
		return
	}
	writeHTML(w, http.StatusOK, successHTML())
}

func (a *App) handleSaveLLMSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var payload LLMSetupIn
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	payload.AgentID = strings.TrimSpace(payload.AgentID)
	payload.APIKey = strings.TrimSpace(payload.APIKey)
	if payload.AgentID == "" || len(payload.APIKey) < 10 {
		writeError(w, http.StatusBadRequest, "agent_id is required and api_key must be at least 10 characters")
		return
	}
	if err := a.saveLLMCredentials(payload.AgentID, payload.APIKey); err != nil {
		statusCode := http.StatusInternalServerError
		if strings.Contains(err.Error(), "Unsupported agent") {
			statusCode = http.StatusBadRequest
		}
		writeError(w, statusCode, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
}

func (a *App) handleGmailTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, gmailToolDefinitions)
}

func (a *App) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var payload ChatMessageIn
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	payload.Message = strings.TrimSpace(payload.Message)
	if payload.Message == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}
	conversationID, err := a.resolveConversationID(payload.ConversationID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	response, err := a.processChatMessage(conversationID, payload.Message, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) handleChatSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var payload ChatMessageIn
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	payload.Message = strings.TrimSpace(payload.Message)
	if payload.Message == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}
	conversationID, err := a.resolveConversationID(payload.ConversationID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.startStreamSession(conversationID); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	go func() {
		emit := func(token string) {
			_ = a.emitStreamToken(conversationID, token)
		}
		response, processErr := a.processChatMessage(conversationID, payload.Message, emit)
		if processErr != nil {
			_ = a.emitStreamError(conversationID, processErr.Error())
			return
		}
		_ = a.emitStreamDone(conversationID, response)
	}()
	writeJSON(w, http.StatusOK, ChatSessionStartOut{
		ConversationID: conversationID,
		SessionID:      conversationID,
	})
}

func (a *App) handleChatStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	if !isValidConversationID(sessionID) {
		writeError(w, http.StatusBadRequest, "session_id is invalid")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	backlog, updates, unsubscribe, completed, err := a.subscribeStreamSession(sessionID, parseLastEventID(r))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	for _, event := range backlog {
		if err := writeSSEEvent(w, event); err != nil {
			return
		}
		flusher.Flush()
	}

	if completed {
		return
	}

	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-updates:
			if !ok {
				return
			}
			if err := writeSSEEvent(w, event); err != nil {
				return
			}
			flusher.Flush()
			if event.Event == "done" || event.Event == "failed" {
				return
			}
		case <-keepAlive.C:
			if _, err := io.WriteString(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (a *App) handleConversationMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	prefix := a.config.APIPrefix + "/chat/conversations/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	conversationID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, prefix))
	if conversationID == "" {
		writeError(w, http.StatusBadRequest, "conversation_id is required")
		return
	}
	if !isValidConversationID(conversationID) {
		writeError(w, http.StatusBadRequest, "conversation_id is invalid")
		return
	}
	exists, err := a.conversationExists(conversationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "conversation not found")
		return
	}
	messages, err := a.getConversationMessages(conversationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ConversationMessagesOut{
		ConversationID: conversationID,
		Messages:       messages,
	})
}

func (a *App) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	items, err := a.getActionHistory(100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, HistoryListOut{Items: items})
}

func (a *App) handlePreferences(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		preferences, err := a.getAllPreferences()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, PreferencesOut{Preferences: preferences})
	case http.MethodPost:
		var payload PreferencesIn
		if err := decodeJSON(r, &payload); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := a.savePreferences(payload.Preferences); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to save preferences: %v", err))
			return
		}
		writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
	default:
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (a *App) buildSetupStatus() (SetupStatus, error) {
	state, llmConfigured, err := a.refreshOnboardingState()
	if err != nil {
		return SetupStatus{}, err
	}
	return SetupStatus{
		GoogleClientConfigured: state.GoogleClientConfigured,
		GoogleAccountConnected: state.GoogleAccountConnected,
		GoogleEmail:            state.GoogleEmail,
		LLMConfigured:          llmConfigured,
		SelectedAgentID:        state.SelectedAgentID,
		OnboardingCompleted:    state.OnboardingCompleted,
		AvailableAgents:        agentDefinitions,
		GmailTools:             gmailToolDefinitions,
	}, nil
}

func (a *App) getSetupState() (SetupState, error) {
	row := a.db.QueryRow(`SELECT google_client_configured, google_account_connected, google_email, selected_agent_id, llm_provider, llm_model, onboarding_completed FROM setup_state WHERE id = 1`)
	var state SetupState
	var googleClientConfigured int
	var googleAccountConnected int
	var onboardingCompleted int
	var googleEmail sql.NullString
	var selectedAgentID sql.NullString
	var llmProvider sql.NullString
	var llmModel sql.NullString
	if err := row.Scan(&googleClientConfigured, &googleAccountConnected, &googleEmail, &selectedAgentID, &llmProvider, &llmModel, &onboardingCompleted); err != nil {
		return SetupState{}, err
	}
	state.GoogleClientConfigured = googleClientConfigured != 0
	state.GoogleAccountConnected = googleAccountConnected != 0
	state.OnboardingCompleted = onboardingCompleted != 0
	if googleEmail.Valid {
		value := googleEmail.String
		state.GoogleEmail = &value
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
	if _, err := a.db.Exec(`UPDATE setup_state SET google_client_configured = 1, google_account_connected = 0, google_email = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = 1`); err != nil {
		return err
	}
	_, _, err := a.refreshOnboardingState()
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

func (a *App) buildGoogleAuthURL() (string, error) {
	clientID, err := a.getSecret("google_client_id")
	if err != nil {
		return "", err
	}
	clientSecret, err := a.getSecret("google_client_secret")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(clientSecret) == "" {
		return "", errors.New("Google OAuth client credentials are not configured yet.")
	}
	stateToken, err := randomToken(32)
	if err != nil {
		return "", err
	}
	codeVerifier, err := randomToken(72)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(digest[:])
	if err := a.upsertSecret("google_oauth_state", stateToken); err != nil {
		return "", err
	}
	if err := a.upsertSecret("google_oauth_code_verifier", codeVerifier); err != nil {
		return "", err
	}
	query := url.Values{}
	query.Set("client_id", clientID)
	query.Set("redirect_uri", a.buildRedirectURI())
	query.Set("response_type", "code")
	query.Set("scope", strings.Join([]string{
		"openid",
		"email",
		"https://www.googleapis.com/auth/gmail.modify",
		"https://www.googleapis.com/auth/gmail.labels",
		"https://www.googleapis.com/auth/gmail.settings.basic",
	}, " "))
	query.Set("access_type", "offline")
	query.Set("prompt", "consent")
	query.Set("state", stateToken)
	query.Set("code_challenge", codeChallenge)
	query.Set("code_challenge_method", "S256")
	return googleAuthEndpoint + "?" + query.Encode(), nil
}

func (a *App) completeGoogleOAuthCallback(code string, stateValue string) error {
	savedState, err := a.getSecret("google_oauth_state")
	if err != nil {
		return err
	}
	codeVerifier, err := a.getSecret("google_oauth_code_verifier")
	if err != nil {
		return err
	}
	clientID, err := a.getSecret("google_client_id")
	if err != nil {
		return err
	}
	clientSecret, err := a.getSecret("google_client_secret")
	if err != nil {
		return err
	}
	if savedState == "" || codeVerifier == "" || clientID == "" || clientSecret == "" {
		return errors.New("OAuth setup is incomplete. Save Google client credentials first.")
	}
	if savedState != stateValue {
		return errors.New("OAuth state validation failed.")
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("code", code)
	form.Set("code_verifier", codeVerifier)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", a.buildRedirectURI())
	req, err := http.NewRequest(http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("Google token exchange failed: %s", strings.TrimSpace(string(body)))
	}
	var tokenPayload map[string]any
	if err := json.Unmarshal(body, &tokenPayload); err != nil {
		return err
	}
	accessToken, _ := tokenPayload["access_token"].(string)
	userEmail := ""
	if accessToken != "" {
		userinfoReq, err := http.NewRequest(http.MethodGet, googleUserinfoEndpoint, nil)
		if err != nil {
			return err
		}
		userinfoReq.Header.Set("Authorization", "Bearer "+accessToken)
		userinfoResp, err := a.httpClient.Do(userinfoReq)
		if err != nil {
			return err
		}
		defer userinfoResp.Body.Close()
		userinfoBody, err := io.ReadAll(userinfoResp.Body)
		if err != nil {
			return err
		}
		if userinfoResp.StatusCode >= 400 {
			return fmt.Errorf("Google userinfo request failed: %s", strings.TrimSpace(string(userinfoBody)))
		}
		var userinfoPayload map[string]any
		if err := json.Unmarshal(userinfoBody, &userinfoPayload); err != nil {
			return err
		}
		if email, ok := userinfoPayload["email"].(string); ok {
			userEmail = email
		}
	}
	tokenJSON, err := json.Marshal(tokenPayload)
	if err != nil {
		return err
	}
	if err := a.upsertSecret("google_token_bundle", string(tokenJSON)); err != nil {
		return err
	}
	if _, err := a.db.Exec(`UPDATE setup_state SET google_account_connected = 1, google_email = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1`, nullIfEmpty(userEmail)); err != nil {
		return err
	}
	_, _, err = a.refreshOnboardingState()
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
	actionType = strings.TrimSpace(actionType)
	if actionType == "" {
		actionType = "tool_call"
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		summary = "Tool call executed"
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
	rows, err := a.db.Query(`SELECT id, action_type, summary, created_at FROM action_history ORDER BY created_at DESC LIMIT ?`, limit)
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

func buildChatSystemPrompt(preferences []PreferenceItem) string {
	prompt := strings.TrimSpace(`You are Towel, a careful Gmail organization assistant.

Objectives:
- Help the user triage inboxes, clean clutter, and build sustainable Gmail workflows.
- Prefer safe, reversible actions.

Tool policy:
- Use tools when a claim depends on mailbox state.
- Never invent tool results.
- If tool outputs are partial or placeholder, say so clearly and propose the next safest step.
- Treat destructive actions as pseudo-actions under the Towel/ namespace.

Response style:
- Be concise and practical.
- After any tool usage, summarize what happened and what to do next.`)
	if len(preferences) == 0 {
		return prompt
	}
	lines := make([]string, 0, len(preferences))
	for _, pref := range preferences {
		value := strings.TrimSpace(pref.Value)
		if value == "" {
			continue
		}
		lines = append(lines, "- "+value)
	}
	if len(lines) == 0 {
		return prompt
	}
	return prompt + "\n\nUser preferences:\n" + strings.Join(lines, "\n")
}

func (a *App) processChatMessage(conversationID string, userMessage string, emitToken func(string)) (ChatMessageOut, error) {
	state, err := a.getSetupState()
	if err != nil {
		return ChatMessageOut{}, err
	}
	if state.SelectedAgentID == nil || strings.TrimSpace(*state.SelectedAgentID) == "" {
		return ChatMessageOut{}, errors.New("LLM not configured. Please complete setup first.")
	}
	apiKey, err := a.getSecret("llm_api_key")
	if err != nil {
		return ChatMessageOut{}, err
	}
	if strings.TrimSpace(apiKey) == "" {
		return ChatMessageOut{}, errors.New("LLM API key not found. Please complete setup first.")
	}
	agent, ok := getAgentDefinition(*state.SelectedAgentID)
	if !ok {
		return ChatMessageOut{}, fmt.Errorf("Unsupported agent: %s", *state.SelectedAgentID)
	}
	preferences, err := a.getAllPreferences()
	if err != nil {
		return ChatMessageOut{}, err
	}

	if err := a.ensureConversation(conversationID); err != nil {
		return ChatMessageOut{}, err
	}
	if err := a.saveConversationMessage(conversationID, "user", userMessage); err != nil {
		return ChatMessageOut{}, err
	}

	history, err := a.getConversationMessages(conversationID)
	if err != nil {
		return ChatMessageOut{}, err
	}

	systemPrompt := buildChatSystemPrompt(preferences)
	messages := make([]map[string]any, 0, len(history)+1)
	messages = append(messages, map[string]any{"role": "system", "content": systemPrompt})
	for _, item := range history {
		role := strings.TrimSpace(item.Role)
		if role != "user" && role != "assistant" {
			continue
		}
		messages = append(messages, map[string]any{"role": role, "content": item.Content})
	}

	assistantMessage, actions, err := a.callLLM(agent, apiKey, messages, emitToken)
	if err != nil {
		return ChatMessageOut{}, fmt.Errorf("Chat processing failed: %v", err)
	}
	assistantMessage = strings.TrimSpace(assistantMessage)
	if assistantMessage == "" {
		return ChatMessageOut{}, errors.New("Chat processing failed: model returned an empty response")
	}

	if err := a.saveConversationMessage(conversationID, "assistant", assistantMessage); err != nil {
		return ChatMessageOut{}, err
	}

	return ChatMessageOut{
		ConversationID: conversationID,
		Response:       assistantMessage,
		Actions:        actions,
	}, nil
}

func (a *App) callLLM(agent AgentDefinition, apiKey string, messages []map[string]any, emitToken func(string)) (string, []string, error) {
	tools := buildLLMToolsPayload()
	actions := make([]string, 0)

	for iteration := 0; iteration < maxToolCallIterations; iteration++ {
		message, err := a.callLLMOnce(agent, apiKey, messages, tools)
		if err != nil {
			return "", actions, err
		}

		content := stringifyLLMContent(message.Content)
		if len(message.ToolCalls) == 0 {
			if strings.TrimSpace(content) == "" {
				return "", actions, errors.New("upstream LLM returned empty content")
			}
			emitTokenizedText(content, emitToken)
			return content, actions, nil
		}

		assistantMessage := map[string]any{
			"role":       "assistant",
			"tool_calls": convertToolCalls(message.ToolCalls),
			"content":    "",
		}
		if message.Content != nil {
			assistantMessage["content"] = message.Content
		}
		messages = append(messages, assistantMessage)

		if text := strings.TrimSpace(content); text != "" {
			note := "Assistant: " + text
			actions = append(actions, note)
			emitTokenizedText(note+"\n", emitToken)
		}

		for toolIndex, toolCall := range message.ToolCalls {
			result, action, actionType := a.executeToolCall(toolCall)
			actions = append(actions, action)
			emitTokenizedText(action+"\n", emitToken)
			if err := a.logActionHistory(actionType, action, result); err != nil {
				return "", actions, fmt.Errorf("failed to record tool call: %w", err)
			}

			toolCallID := strings.TrimSpace(toolCall.ID)
			if toolCallID == "" {
				toolCallID = fmt.Sprintf("toolcall_%d_%d", iteration, toolIndex)
			}
			messages = append(messages, map[string]any{
				"role":         "tool",
				"tool_call_id": toolCallID,
				"content":      result,
			})
		}
	}

	return "", actions, fmt.Errorf("upstream LLM exceeded tool-call iteration limit (%d)", maxToolCallIterations)
}

func buildLLMToolsPayload() []map[string]any {
	tools := make([]map[string]any, 0, len(gmailToolDefinitions))
	for _, tool := range gmailToolDefinitions {
		functionName := strings.ReplaceAll(tool.Name, ".", "_")
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        functionName,
				"description": tool.Description,
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
		})
	}
	return tools
}

func convertToolCalls(toolCalls []llmToolCall) []map[string]any {
	converted := make([]map[string]any, 0, len(toolCalls))
	for _, call := range toolCalls {
		callType := strings.TrimSpace(call.Type)
		if callType == "" {
			callType = "function"
		}
		converted = append(converted, map[string]any{
			"id":   call.ID,
			"type": callType,
			"function": map[string]any{
				"name":      call.Function.Name,
				"arguments": call.Function.Arguments,
			},
		})
	}
	return converted
}

func (a *App) callLLMOnce(agent AgentDefinition, apiKey string, messages []map[string]any, tools []map[string]any) (llmResponseMessage, error) {
	requestPayload := map[string]any{
		"model":                 agent.Model,
		"messages":              messages,
		"temperature":           0.7,
		"max_completion_tokens": 1000,
		"tools":                 tools,
		"tool_choice":           "auto",
	}
	body, err := json.Marshal(requestPayload)
	if err != nil {
		return llmResponseMessage{}, err
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(agent.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return llmResponseMessage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return llmResponseMessage{}, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return llmResponseMessage{}, err
	}
	if resp.StatusCode >= 400 {
		return llmResponseMessage{}, fmt.Errorf("upstream LLM request failed: %s", strings.TrimSpace(string(responseBody)))
	}
	var payload struct {
		Choices []struct {
			Message llmResponseMessage `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		return llmResponseMessage{}, err
	}
	if len(payload.Choices) == 0 {
		return llmResponseMessage{}, errors.New("upstream LLM response did not include any choices")
	}
	return payload.Choices[0].Message, nil
}

func (a *App) executeToolCall(call llmToolCall) (string, string, string) {
	argumentsText := strings.TrimSpace(call.Function.Arguments)
	arguments := map[string]any{}
	if argumentsText != "" {
		if err := json.Unmarshal([]byte(argumentsText), &arguments); err != nil {
			arguments = map[string]any{"raw": argumentsText}
		}
	}

	toolName := call.Function.Name
	safetyModel := "unknown"
	toolDescription := "Tool definition not found."
	if definition, ok := getToolDefinitionByFunctionName(call.Function.Name); ok {
		toolName = definition.Name
		safetyModel = definition.SafetyModel
		toolDescription = definition.Description
	}

	// Execute real Gmail tool if available
	var resultJSON string
	var execErr error
	if strings.HasPrefix(toolName, "gmail.") {
		resultJSON, execErr = a.executeGmailTool(toolName, arguments)
	}

	if execErr != nil {
		// Return error as result so LLM can see what went wrong
		resultPayload := map[string]any{
			"ok":           false,
			"tool":         toolName,
			"safety_model": safetyModel,
			"description":  toolDescription,
			"arguments":    arguments,
			"error":        execErr.Error(),
		}
		resultBytes, _ := json.Marshal(resultPayload)
		action := fmt.Sprintf("Failed to execute %s: %s", toolName, truncateString(execErr.Error(), 120))
		return string(resultBytes), action, toolName
	}

	if resultJSON == "" {
		// Fallback for non-Gmail tools or unexpected cases
		resultPayload := map[string]any{
			"ok":           true,
			"tool":         toolName,
			"safety_model": safetyModel,
			"description":  toolDescription,
			"arguments":    arguments,
			"result":       "Tool executed (no specific implementation for this tool).",
		}
		resultBytes, _ := json.Marshal(resultPayload)
		resultJSON = string(resultBytes)
	}

	action := "Executed tool " + toolName + "."
	if argumentsText != "" {
		action = fmt.Sprintf("Executed tool %s with args %s.", toolName, truncateString(argumentsText, 180))
	}

	return resultJSON, action, toolName
}

func getToolDefinitionByFunctionName(functionName string) (GmailToolDefinition, bool) {
	for _, tool := range gmailToolDefinitions {
		if strings.ReplaceAll(tool.Name, ".", "_") == functionName {
			return tool, true
		}
	}
	return GmailToolDefinition{}, false
}

func (a *App) startStreamSession(sessionID string) error {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	a.pruneStreamSessionsLocked()

	if existing, ok := a.streamSessions[sessionID]; ok {
		if !existing.Completed {
			return errors.New("a stream is already active for this conversation")
		}
		for subscriberID, ch := range existing.Subscribers {
			close(ch)
			delete(existing.Subscribers, subscriberID)
		}
	}

	a.streamSessions[sessionID] = &streamSession{
		ID:          sessionID,
		Events:      make([]streamEvent, 0, 128),
		NextEventID: 1,
		Subscribers: make(map[int64]chan streamEvent),
		UpdatedAt:   time.Now().UTC(),
	}
	return nil
}

func (a *App) subscribeStreamSession(sessionID string, lastEventID int64) ([]streamEvent, <-chan streamEvent, func(), bool, error) {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	a.pruneStreamSessionsLocked()

	session, ok := a.streamSessions[sessionID]
	if !ok {
		return nil, nil, nil, false, errors.New("stream session not found")
	}

	backlog := make([]streamEvent, 0, len(session.Events))
	for _, event := range session.Events {
		if event.ID > lastEventID {
			backlog = append(backlog, event)
		}
	}

	if session.Completed {
		return backlog, nil, func() {}, true, nil
	}

	a.nextSubscriberID++
	subscriberID := a.nextSubscriberID
	updates := make(chan streamEvent, 128)
	session.Subscribers[subscriberID] = updates
	session.UpdatedAt = time.Now().UTC()

	unsubscribe := func() {
		a.streamMu.Lock()
		defer a.streamMu.Unlock()
		session, exists := a.streamSessions[sessionID]
		if !exists {
			return
		}
		ch, exists := session.Subscribers[subscriberID]
		if !exists {
			return
		}
		delete(session.Subscribers, subscriberID)
		close(ch)
	}

	return backlog, updates, unsubscribe, false, nil
}

func (a *App) publishStreamEvent(sessionID string, eventName string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	session, ok := a.streamSessions[sessionID]
	if !ok {
		return errors.New("stream session not found")
	}

	event := streamEvent{
		ID:    session.NextEventID,
		Event: eventName,
		Data:  encoded,
	}
	session.NextEventID++
	session.Events = append(session.Events, event)
	session.UpdatedAt = time.Now().UTC()
	if eventName == "done" || eventName == "failed" {
		session.Completed = true
	}

	for _, updates := range session.Subscribers {
		select {
		case updates <- event:
		default:
		}
	}

	return nil
}

func (a *App) emitStreamToken(sessionID string, token string) error {
	if token == "" {
		return nil
	}
	return a.publishStreamEvent(sessionID, "token", map[string]string{"v": token})
}

func (a *App) emitStreamDone(sessionID string, response ChatMessageOut) error {
	return a.publishStreamEvent(sessionID, "done", response)
}

func (a *App) emitStreamError(sessionID string, detail string) error {
	return a.publishStreamEvent(sessionID, "failed", errorResponse{Detail: detail})
}

func (a *App) pruneStreamSessionsLocked() {
	cutoff := time.Now().UTC().Add(-streamSessionTTL)
	for sessionID, session := range a.streamSessions {
		if session.UpdatedAt.After(cutoff) {
			continue
		}
		for subscriberID, updates := range session.Subscribers {
			close(updates)
			delete(session.Subscribers, subscriberID)
		}
		delete(a.streamSessions, sessionID)
	}
}

func parseLastEventID(r *http.Request) int64 {
	raw := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("last_event_id"))
	}
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func writeSSEEvent(w io.Writer, event streamEvent) error {
	if event.Event != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", event.Event); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "id: %d\n", event.ID); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", event.Data); err != nil {
		return err
	}
	return nil
}

func emitTokenizedText(value string, emitToken func(string)) {
	if emitToken == nil || value == "" {
		return
	}
	for _, token := range splitTextTokens(value) {
		emitToken(token)
	}
}

func splitTextTokens(value string) []string {
	tokens := make([]string, 0)
	var builder strings.Builder
	runeCount := 0
	flush := func() {
		if builder.Len() == 0 {
			return
		}
		tokens = append(tokens, builder.String())
		builder.Reset()
		runeCount = 0
	}
	for _, r := range value {
		builder.WriteRune(r)
		runeCount++
		if r == ' ' || r == '\n' || runeCount >= 24 {
			flush()
		}
	}
	flush()
	return tokens
}

func stringifyLLMContent(content any) string {
	switch value := content.(type) {
	case nil:
		return ""
	case string:
		return value
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			if piece, ok := item.(map[string]any); ok {
				if text, ok := piece["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "")
		}
		encoded, _ := json.Marshal(value)
		if string(encoded) == "null" {
			return ""
		}
		return string(encoded)
	default:
		encoded, _ := json.Marshal(value)
		if string(encoded) == "null" {
			return ""
		}
		return string(encoded)
	}
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

func (a *App) buildRedirectURI() string {
	return strings.TrimRight(a.config.PublicAPIBaseURL, "/") + a.config.APIPrefix + "/setup/google/callback"
}

func getAgentDefinition(agentID string) (AgentDefinition, bool) {
	for _, agent := range agentDefinitions {
		if agent.AgentID == agentID {
			return agent, true
		}
	}
	return AgentDefinition{}, false
}

func randomToken(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
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

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid request body: %w", err)
	}
	if decoder.More() {
		return errors.New("invalid request body: unexpected trailing data")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, errorResponse{Detail: detail})
}

func writeHTML(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func successHTML() string {
	return "<!DOCTYPE html><html><head><meta http-equiv=\"refresh\" content=\"0; url=" + frontendSetupURL + "?oauth=success\"><script>window.location.href = \"" + frontendSetupURL + "?oauth=success\";</script></head><body style=\"font-family:-apple-system,BlinkMacSystemFont,'SF Pro Text',Arial,sans-serif;background:#f5f5f7;color:#1d1d1f;padding:40px;text-align:center;\"><h2>Connected successfully</h2><p>Redirecting back to setup...</p></body></html>"
}

func failureHTML(message string) string {
	escapedMessage := html.EscapeString(message)
	redirectURL := frontendSetupURL + "?oauth=error&msg=" + url.QueryEscape(message)
	return "<!DOCTYPE html><html><head><meta http-equiv=\"refresh\" content=\"3; url=" + redirectURL + "\"></head><body style=\"font-family:-apple-system,BlinkMacSystemFont,'SF Pro Text',Arial,sans-serif;background:#ffebe9;color:#ff3b30;padding:40px;text-align:center;\"><h2>Connection failed</h2><p>" + escapedMessage + "</p><p>Redirecting back...</p></body></html>"
}
