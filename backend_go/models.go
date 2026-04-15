package main

import (
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
