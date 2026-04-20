package main

import (
	"context"
	"database/sql"
	"net/http"
	"sync"
	"time"
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

	emailSyncMu      sync.Mutex
	emailSyncRunning bool

	emailEmbeddingMu                       sync.Mutex
	emailEmbeddingRunning                  bool
	emailEmbeddingPending                  bool
	emailEmbeddingPendingReason            string
	emailEmbeddingPendingReset             bool
	emailEmbeddingPendingFullRescan        bool
	emailEmbeddingPendingMessageIDs        map[string]struct{}
	emailEmbeddingPendingDeletedMessageIDs map[string]struct{}

	googleChatMu     sync.Mutex
	googleChatCancel context.CancelFunc
	googleChatRunID  int64
	googleChatState  GoogleChatRuntimeState

	streamMu         sync.Mutex
	streamSessions   map[string]*streamSession
	nextSubscriberID int64
}

type GoogleChatRuntimeState struct {
	Running       bool
	LastError     string
	LastEventType string
	LastEventAt   string
	LastReplyAt   string
}

type AgentDefinition struct {
	AgentID       string `json:"agent_id"`
	Provider      string `json:"provider"`
	AuthMode      string `json:"auth_mode"`
	Label         string `json:"label"`
	Model         string `json:"model"`
	ReasoningMode string `json:"reasoning_mode"`
	Verbosity     string `json:"verbosity"`
	BaseURL       string `json:"-"`
}

type GmailToolDefinition struct {
	Name         string         `json:"name"`
	GmailActions []string       `json:"gmail_actions"`
	Description  string         `json:"description"`
	SafetyModel  string         `json:"safety_model"`
	Parameters   map[string]any `json:"parameters,omitempty"`
}

type UserSession struct {
	ID      string
	Email   string
	Name    string
	Picture string
}

type SetupState struct {
	GoogleClientConfigured bool
	GoogleAccountConnected bool
	GoogleEmail            *string
	GoogleName             *string
	GooglePicture          *string
	SelectedAgentID        *string
	LLMProvider            *string
	LLMModel               *string
	OnboardingCompleted    bool
}

type SetupStatus struct {
	GoogleClientConfigured bool                  `json:"google_client_configured"`
	GoogleAccountConnected bool                  `json:"google_account_connected"`
	GoogleEmail            *string               `json:"google_email"`
	GoogleName             *string               `json:"google_name"`
	GooglePicture          *string               `json:"google_picture"`
	EmailSync              EmailSyncStatus       `json:"email_sync"`
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

type GeminiSetupIn struct {
	AgentID string `json:"agent_id"`
}

type GeminiSetupOut struct {
	Success     bool   `json:"success"`
	Status      string `json:"status"`
	Detail      string `json:"detail,omitempty"`
	EnableURL   string `json:"enable_url,omitempty"`
	NeedsReauth bool   `json:"needs_reauth,omitempty"`
}

type SettingsAgent struct {
	AgentID       string `json:"agent_id"`
	Provider      string `json:"provider"`
	AuthMode      string `json:"auth_mode"`
	Label         string `json:"label"`
	Model         string `json:"model"`
	ReasoningMode string `json:"reasoning_mode"`
	Verbosity     string `json:"verbosity"`
	BaseURL       string `json:"base_url"`
	IsCustom      bool   `json:"is_custom"`
}

type SettingsOut struct {
	SelectedAgentID *string               `json:"selected_agent_id"`
	HasAPIKey       bool                  `json:"has_api_key"`
	Agents          []SettingsAgent       `json:"agents"`
	GoogleChat      GoogleChatSettingsOut `json:"google_chat"`
}

type GoogleChatSettingsOut struct {
	Enabled               bool   `json:"enabled"`
	Configured            bool   `json:"configured"`
	Running               bool   `json:"running"`
	ProjectID             string `json:"project_id"`
	TopicID               string `json:"topic_id"`
	SubscriptionID        string `json:"subscription_id"`
	HasServiceAccountJSON bool   `json:"has_service_account_json"`
	ServiceAccountEmail   string `json:"service_account_email,omitempty"`
	LastError             string `json:"last_error,omitempty"`
	LastEventType         string `json:"last_event_type,omitempty"`
	LastEventAt           string `json:"last_event_at,omitempty"`
	LastReplyAt           string `json:"last_reply_at,omitempty"`
}

type SettingsAgentInput struct {
	AgentID       string `json:"agent_id"`
	Provider      string `json:"provider"`
	AuthMode      string `json:"auth_mode"`
	Label         string `json:"label"`
	Model         string `json:"model"`
	ReasoningMode string `json:"reasoning_mode"`
	Verbosity     string `json:"verbosity"`
	BaseURL       string `json:"base_url"`
}

type SettingsIn struct {
	SelectedAgentID *string               `json:"selected_agent_id"`
	APIKey          string                `json:"api_key"`
	Agents          []SettingsAgentInput  `json:"agents"`
	GoogleChat      *GoogleChatSettingsIn `json:"google_chat,omitempty"`
}

type GoogleChatSettingsIn struct {
	Enabled            bool   `json:"enabled"`
	ProjectID          string `json:"project_id"`
	TopicID            string `json:"topic_id"`
	SubscriptionID     string `json:"subscription_id"`
	ServiceAccountJSON string `json:"service_account_json"`
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

type ChatSessionStopIn struct {
	SessionID string `json:"session_id"`
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

type ConversationSummary struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updated_at"`
}

type ConversationListOut struct {
	Items    []ConversationSummary `json:"items"`
	Page     int                   `json:"page"`
	PageSize int                   `json:"page_size"`
	HasMore  bool                  `json:"has_more"`
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
	Cancel      context.CancelFunc
	Completed   bool
	Canceled    bool
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
		AuthMode:      "api_key",
		Label:         "OpenAI GPT 5.4",
		Model:         "gpt-5.4",
		ReasoningMode: "thinking",
		Verbosity:     "low",
		BaseURL:       "https://api.openai.com/v1",
	},
	{
		AgentID:       "openai:gpt-5.4-mini",
		Provider:      "openai",
		AuthMode:      "api_key",
		Label:         "OpenAI GPT 5.4 Mini",
		Model:         "gpt-5.4-mini",
		ReasoningMode: "thinking",
		Verbosity:     "low",
		BaseURL:       "https://api.openai.com/v1",
	},
	{
		AgentID:       "gemini:gemini-3-flash-preview",
		Provider:      "gemini",
		AuthMode:      "google_oauth",
		Label:         "Google Gemini 3 Flash",
		Model:         "gemini-3-flash-preview",
		ReasoningMode: "thinking",
		Verbosity:     "low",
		BaseURL:       "https://generativelanguage.googleapis.com/v1beta",
	},
	{
		AgentID:       "deepseek:deepseek-thinking",
		Provider:      "deepseek",
		AuthMode:      "api_key",
		Label:         "DeepSeek Thinking",
		Model:         "deepseek-reasoner",
		ReasoningMode: "thinking",
		Verbosity:     "low",
		BaseURL:       "https://api.deepseek.com/v1",
	},
}
