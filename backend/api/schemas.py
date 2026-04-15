from pydantic import BaseModel, Field


class AgentDefinition(BaseModel):
    agent_id: str
    provider: str
    label: str
    model: str
    reasoning_mode: str
    verbosity: str


class GmailToolDefinition(BaseModel):
    name: str
    gmail_actions: list[str]
    description: str
    safety_model: str


class SetupStatus(BaseModel):
    google_client_configured: bool
    google_account_connected: bool
    google_email: str | None
    llm_configured: bool
    selected_agent_id: str | None
    onboarding_completed: bool
    available_agents: list[AgentDefinition]
    gmail_tools: list[GmailToolDefinition]


class GoogleOAuthCredentialsIn(BaseModel):
    client_id: str = Field(min_length=10)
    client_secret: str = Field(min_length=10)


class GoogleAuthUrlOut(BaseModel):
    auth_url: str


class LLMSetupIn(BaseModel):
    agent_id: str
    api_key: str = Field(min_length=10)


class SuccessResponse(BaseModel):
    success: bool = True


class ChatMessageIn(BaseModel):
    message: str = Field(min_length=1)


class ChatMessageOut(BaseModel):
    response: str
    actions: list[str] = []


class HistoryItem(BaseModel):
    id: int
    action: str
    details: str
    timestamp: str


class HistoryListOut(BaseModel):
    items: list[HistoryItem]


class PreferenceItem(BaseModel):
    id: int
    label: str
    value: str
    created_at: str
    updated_at: str


class PreferencesOut(BaseModel):
    preferences: list[PreferenceItem]


class PreferenceInput(BaseModel):
    id: int | None = None
    value: str


class PreferencesIn(BaseModel):
    preferences: list[PreferenceInput]
