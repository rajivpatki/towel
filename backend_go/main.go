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
	"strings"
	"time"

	"github.com/fernet/fernet-go"
	_ "modernc.org/sqlite"
)

const (
	googleAuthEndpoint     = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenEndpoint    = "https://oauth2.googleapis.com/token"
	googleUserinfoEndpoint = "https://openidconnect.googleapis.com/v1/userinfo"
	frontendSetupURL       = "http://localhost:3000/setup/gmail"
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
	Name        string   `json:"name"`
	GmailActions []string `json:"gmail_actions"`
	Description string   `json:"description"`
	SafetyModel string   `json:"safety_model"`
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
	Message string `json:"message"`
}

type ChatMessageOut struct {
	Response string   `json:"response"`
	Actions  []string `json:"actions"`
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
	}
	if err := app.initDB(); err != nil {
		db.Close()
		return nil, err
	}
	return app, nil
}

func (a *App) initDB() error {
	statements := []string{
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
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_secret_records_key ON secret_records(key);`,
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
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
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
	response, err := a.processChatMessage(payload.Message)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
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
	req, err := http.NewRequest(http.MethodPost, googleTokenEndpoint, strings.NewReader(form.Encode()))
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

func (a *App) processChatMessage(userMessage string) (ChatMessageOut, error) {
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
	systemPrompt := "You are Towel, an AI assistant for Gmail organization. You help users manage their emails safely."
	if len(preferences) > 0 {
		lines := make([]string, 0, len(preferences))
		for _, pref := range preferences {
			lines = append(lines, "- "+pref.Value)
		}
		systemPrompt += "\n\nUser preferences:\n" + strings.Join(lines, "\n")
	}
	assistantMessage, err := a.callLLM(agent, apiKey, systemPrompt, userMessage)
	if err != nil {
		return ChatMessageOut{}, fmt.Errorf("Chat processing failed: %v", err)
	}
	var payload any
	if strings.TrimSpace(assistantMessage) != "" {
		payload = truncateString(assistantMessage, 500)
	}
	if _, err := a.db.Exec(`INSERT INTO action_history (action_type, summary, payload) VALUES (?, ?, ?)`, "chat_query", truncateString(userMessage, 200), payload); err != nil {
		return ChatMessageOut{}, err
	}
	return ChatMessageOut{
		Response: assistantMessage,
		Actions:  []string{},
	}, nil
}

func (a *App) callLLM(agent AgentDefinition, apiKey string, systemPrompt string, userMessage string) (string, error) {
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

	requestPayload := map[string]any{
		"model": agent.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userMessage},
		},
		"temperature": 0.7,
		"max_completion_tokens": 1000,
		"tools":               tools,
	}
	body, err := json.Marshal(requestPayload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(agent.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("upstream LLM request failed: %s", strings.TrimSpace(string(responseBody)))
	}
	var payload struct {
		Choices []struct {
			Message struct {
				Content    any  `json:"content"`
				ToolCalls  []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		return "", err
	}
	if len(payload.Choices) == 0 {
		return "", errors.New("upstream LLM response did not include any choices")
	}
	msg := payload.Choices[0].Message
	if len(msg.ToolCalls) > 0 {
		parts := []string{"I want to use these tools:"}
		for _, tc := range msg.ToolCalls {
			parts = append(parts, fmt.Sprintf("- %s(%s)", tc.Function.Name, tc.Function.Arguments))
		}
		return strings.Join(parts, "\n"), nil
	}
	return stringifyLLMContent(msg.Content), nil
}

func stringifyLLMContent(content any) string {
	switch value := content.(type) {
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
		return string(encoded)
	default:
		encoded, _ := json.Marshal(value)
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
