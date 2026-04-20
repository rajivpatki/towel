import { useEffect, useMemo, useState } from 'react'
import { useToast } from '../components/ToastProvider'
import CustomSelect from '../components/CustomSelect'

const apiBaseUrl = import.meta.env.VITE_API_BASE_URL || `http://localhost:8000`
const googleChatLinks = {
  enableApis: 'https://console.cloud.google.com/flows/enableapi?apiid=chat.googleapis.com,pubsub.googleapis.com',
  chatApiConfiguration: 'https://console.cloud.google.com/apis/api/chat.googleapis.com/hangouts-chat',
  createTopic: 'https://console.cloud.google.com/cloudpubsub/topic/list',
  createSubscription: 'https://console.cloud.google.com/cloudpubsub/subscription',
  createServiceAccount: 'https://console.cloud.google.com/iam-admin/serviceaccounts/create',
  serviceAccountAuthDocs: 'https://developers.google.com/workspace/chat/api/guides/auth/service-accounts'
}

const defaultGoogleChatStatus = {
  enabled: false,
  configured: false,
  running: false,
  project_id: '',
  topic_id: 'chat-events',
  subscription_id: '',
  has_service_account_json: false,
  service_account_email: '',
  last_error: '',
  last_event_type: '',
  last_event_at: '',
  last_reply_at: ''
}

async function parseResponse(response) {
  if (response.ok) {
    if (response.status === 204) {
      return null
    }
    return response.json()
  }

  if (response.status === 401) {
    window.location.href = '/setup/google'
    throw new Error('Session expired. Please sign in again.')
  }

  let detail = 'Request failed'
  try {
    const body = await response.json()
    detail = body.detail || detail
  } catch {
    detail = response.statusText || detail
  }
  throw new Error(detail)
}

function normalizeAgent(agent) {
  return {
    ...agent,
    auth_mode: agent?.auth_mode || (agent?.provider === 'gemini' ? 'google_oauth' : 'api_key')
  }
}

function Settings() {
  const { showToast } = useToast()
  const [loading, setLoading] = useState(true)
  const [savingModelSettings, setSavingModelSettings] = useState(false)
  const [savingGoogleChat, setSavingGoogleChat] = useState(false)
  const [restartingGoogleChat, setRestartingGoogleChat] = useState(false)
  const [selectedAgentId, setSelectedAgentId] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [hasApiKey, setHasApiKey] = useState(false)
  const [agents, setAgents] = useState([])
  const [googleChat, setGoogleChat] = useState({
    enabled: false,
    project_id: '',
    topic_id: 'chat-events',
    subscription_id: '',
    service_account_json: '',
    status: defaultGoogleChatStatus
  })

  const selectedAgent = useMemo(() => agents.find((agent) => agent.agent_id === selectedAgentId) || null, [agents, selectedAgentId])
  const selectedUsesAPIKey = selectedAgent ? selectedAgent.auth_mode !== 'google_oauth' : true

  async function loadSettings() {
    try {
      const response = await fetch(`${apiBaseUrl}/api/settings`, {
        credentials: 'include'
      })
      const data = await parseResponse(response)
      const nextGoogleChatStatus = {
        ...defaultGoogleChatStatus,
        ...(data.google_chat || {})
      }
      setSelectedAgentId(data.selected_agent_id || data.agents?.[0]?.agent_id || '')
      setHasApiKey(Boolean(data.has_api_key))
      setAgents(Array.isArray(data.agents) ? data.agents.map(normalizeAgent) : [])
      setGoogleChat((current) => ({
        ...current,
        enabled: Boolean(nextGoogleChatStatus.enabled),
        project_id: nextGoogleChatStatus.project_id || '',
        topic_id: nextGoogleChatStatus.topic_id || 'chat-events',
        subscription_id: nextGoogleChatStatus.subscription_id || '',
        service_account_json: '',
        status: nextGoogleChatStatus
      }))
    } catch (err) {
      showToast({
        tone: 'error',
        title: 'Unable to load settings',
        description: err.message
      })
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    loadSettings()
  }, [])

  function buildSettingsPayload({ includeAPIKey, includeGoogleChat }) {
    return {
      selected_agent_id: selectedAgentId || null,
      api_key: includeAPIKey ? apiKey : '',
      agents: agents
        .filter((agent) => agent.is_custom)
        .map((agent) => ({
          agent_id: agent.agent_id,
          provider: agent.provider,
          auth_mode: agent.auth_mode,
          label: agent.label,
          model: agent.model,
          reasoning_mode: agent.reasoning_mode,
          verbosity: agent.verbosity,
          base_url: agent.base_url
        })),
      google_chat: includeGoogleChat
        ? {
            enabled: googleChat.enabled,
            project_id: googleChat.project_id,
            topic_id: googleChat.topic_id,
            subscription_id: googleChat.subscription_id,
            service_account_json: googleChat.service_account_json
          }
        : undefined
    }
  }

  async function saveModelSettings() {
    setSavingModelSettings(true)
    try {
      const response = await fetch(`${apiBaseUrl}/api/settings`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json'
        },
        credentials: 'include',
        body: JSON.stringify(buildSettingsPayload({
          includeAPIKey: true,
          includeGoogleChat: false
        }))
      })
      await parseResponse(response)
      setApiKey('')
      await loadSettings()
      showToast({
        tone: 'success',
        title: 'Model settings saved',
        description: selectedUsesAPIKey
          ? 'Your active model and API key were updated.'
          : 'Your active model was updated. Gemini continues to use your Google OAuth connection.'
      })
    } catch (err) {
      showToast({
        tone: 'error',
        title: 'Unable to save model settings',
        description: err.message
      })
    } finally {
      setSavingModelSettings(false)
    }
  }

  async function saveGoogleChatSettings() {
    setSavingGoogleChat(true)
    try {
      const response = await fetch(`${apiBaseUrl}/api/settings`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json'
        },
        credentials: 'include',
        body: JSON.stringify(buildSettingsPayload({
          includeAPIKey: false,
          includeGoogleChat: true
        }))
      })
      await parseResponse(response)
      setGoogleChat((current) => ({
        ...current,
        service_account_json: ''
      }))
      await loadSettings()
      showToast({
        tone: 'success',
        title: 'Google Chat settings saved',
        description: 'Your Google Chat listener configuration was updated.'
      })
    } catch (err) {
      showToast({
        tone: 'error',
        title: 'Unable to save Google Chat settings',
        description: err.message
      })
    } finally {
      setSavingGoogleChat(false)
    }
  }

  async function restartGoogleChat() {
    setRestartingGoogleChat(true)
    try {
      const response = await fetch(`${apiBaseUrl}/api/settings/google-chat/restart`, {
        method: 'POST',
        credentials: 'include'
      })
      const status = await parseResponse(response)
      setGoogleChat((current) => ({
        ...current,
        status: {
          ...defaultGoogleChatStatus,
          ...status
        }
      }))
      showToast({
        tone: 'success',
        title: 'Google Chat listener restarted',
        description: status.running ? 'The listener is running.' : 'The listener restart completed, but it is not running.'
      })
    } catch (err) {
      showToast({
        tone: 'error',
        title: 'Unable to restart Google Chat listener',
        description: err.message
      })
    } finally {
      setRestartingGoogleChat(false)
    }
  }

  if (loading) {
    return (
      <div className="settings-page">
        <div className="settings-hero">
          <p className="eyebrow">Settings</p>
          <h2>Model & API configuration</h2>
          <p className="settings-copy">Loading your current AI configuration...</p>
        </div>
      </div>
    )
  }

  return (
    <div className="settings-page">
      <div className="settings-hero">
        <p className="eyebrow">Settings</p>
        <h2>Model & API configuration</h2>
        <p className="settings-copy">
          Manage your active model, rotate API credentials, and control Google Chat from one place.
        </p>
      </div>

      <section className="panel settings-section">
        <div className="panel-header settings-panel-header">
          <div>
            <h3>Active model & API key</h3>
            <p className="settings-section-copy">Choose which configured model powers chat responses and save the API key it uses.</p>
          </div>
          {hasApiKey ? <span className="status-pill ok">API key configured</span> : <span className="status-pill pending">API key missing</span>}
        </div>

        <div className="settings-model-card">
          <label>
            <span>Selected model</span>
            <CustomSelect
              value={selectedAgentId}
              onChange={(value) => setSelectedAgentId(value)}
              options={agents.map((agent) => ({
                value: agent.agent_id,
                label: agent.label
              }))}
              placeholder="Select a model..."
              label="Selected model"
            />
          </label>

          {selectedAgent ? (
            <div className="settings-selected-card">
              <div>
                <strong>{selectedAgent.label}</strong>
                <p>{selectedAgent.provider} - {selectedAgent.model}</p>
              </div>
              <div className="agent-badges">
                <span className={`status-pill ${selectedUsesAPIKey ? 'ok' : 'pending'}`}>
                  {selectedUsesAPIKey ? 'API key' : 'Google OAuth'}
                </span>
                <span className="status-pill ok">{selectedAgent.reasoning_mode}</span>
                <span className="status-pill ok">{selectedAgent.verbosity}</span>
                {selectedAgent.is_custom ? <span className="status-pill pending">Custom</span> : null}
              </div>
            </div>
          ) : null}

          <label>
            <span>New API key</span>
            <input
              type="password"
              value={apiKey}
              onChange={(event) => setApiKey(event.target.value)}
              placeholder={hasApiKey ? 'Leave blank to keep existing key' : 'Paste your API key'}
            />
          </label>

          <div className="settings-google-chat-actions">
            <button
              type="button"
              className="secondary"
              onClick={() => {
                setApiKey('')
                loadSettings()
              }}
              disabled={savingModelSettings}
            >
              Reset
            </button>
            <button type="button" onClick={saveModelSettings} disabled={savingModelSettings || !selectedAgentId}>
              {savingModelSettings ? 'Saving...' : 'Save model & key'}
            </button>
          </div>
        </div>
      </section>

      <section className="panel settings-section settings-custom-section">
        <div className="panel-header settings-panel-header">
          <div>
            <h3>Google Chat</h3>
            <p className="settings-section-copy">Connect Google Chat to receive messages via Pub/Sub and send replies through the Chat API.</p>
          </div>
          <div className="agent-badges">
            <span className={`status-pill ${googleChat.status.running ? 'ok' : 'pending'}`}>
              {googleChat.status.running ? 'Running' : 'Stopped'}
            </span>
            {googleChat.status.service_account_email ? <span className="status-pill ok">{googleChat.status.service_account_email}</span> : null}
          </div>
        </div>

        <div style={{ display: 'grid', gap: '16px' }}>
          <div className="info-box">
            <p><strong>Setup steps:</strong></p>
            <ol style={{ marginTop: '8px', paddingLeft: '20px', lineHeight: '1.6' }}>
              <li><a href={googleChatLinks.enableApis} target="_blank" rel="noopener noreferrer">Enable Chat + Pub/Sub APIs</a> in your Google Cloud project</li>
              <li><a href={googleChatLinks.createTopic} target="_blank" rel="noopener noreferrer">Create a Pub/Sub topic</a> named <strong>chat-events</strong></li>
              <li><a href={googleChatLinks.chatApiConfiguration} target="_blank" rel="noopener noreferrer">Configure the Chat API</a>: set connection to "Receive messages via Pub/Sub" and select topic <strong>chat-events</strong></li>
              <li>Grant <strong>chat-api-push@system.gserviceaccount.com</strong> the <strong>Pub/Sub Publisher</strong> role on the <strong>chat-events</strong> topic</li>
              <li><a href={googleChatLinks.createSubscription} target="_blank" rel="noopener noreferrer">Create a pull subscription</a> on the <strong>chat-events</strong> topic (e.g., <strong>chat-events-pull</strong>)</li>
              <li><a href={googleChatLinks.createServiceAccount} target="_blank" rel="noopener noreferrer">Create a service account</a> with <strong>Pub/Sub Subscriber</strong> role on the subscription and download the JSON key</li>
              <li>Paste the service account JSON below and save</li>
            </ol>
            <p style={{ marginTop: '12px', fontSize: '14px', opacity: 0.8 }}>See <a href={googleChatLinks.serviceAccountAuthDocs} target="_blank" rel="noopener noreferrer">service account authentication docs</a> for more details.</p>
            {googleChat.status.last_error ? (
              <p style={{ marginTop: '8px' }}><strong>Last error:</strong> {googleChat.status.last_error}</p>
            ) : null}
            {googleChat.status.last_event_type ? (
              <p style={{ marginTop: '8px' }}><strong>Last event:</strong> {googleChat.status.last_event_type}{googleChat.status.last_event_at ? ` at ${googleChat.status.last_event_at}` : ''}</p>
            ) : null}
            {googleChat.status.last_reply_at ? (
              <p style={{ marginTop: '8px' }}><strong>Last reply:</strong> {googleChat.status.last_reply_at}</p>
            ) : null}
          </div>

          <div className="settings-model-card">
            <div className="settings-model-card-header">
              <div>
                <strong>Enable Google Chat</strong>
                <p>Turn the Pub/Sub listener on or off and manage the service-account-backed backend worker.</p>
              </div>
              <div className="settings-google-chat-actions">
                <label className="settings-toggle" aria-label="Enable Google Chat">
                  <input
                    type="checkbox"
                    checked={googleChat.enabled}
                    onChange={(event) => setGoogleChat((current) => ({ ...current, enabled: event.target.checked }))}
                  />
                  <span className="settings-toggle-track" aria-hidden="true">
                    <span className="settings-toggle-thumb" />
                  </span>
                  <span className="settings-toggle-label">{googleChat.enabled ? 'Enabled' : 'Disabled'}</span>
                </label>
                <button type="button" className="secondary" onClick={restartGoogleChat} disabled={restartingGoogleChat}>
                  {restartingGoogleChat ? 'Restarting...' : 'Restart listener'}
                </button>
              </div>
            </div>

            <div className="settings-model-grid">
              <label>
                <span>Google Cloud project ID</span>
                <input
                  value={googleChat.project_id}
                  onChange={(event) => setGoogleChat((current) => ({ ...current, project_id: event.target.value }))}
                  placeholder="my-gcp-project"
                />
              </label>
              <label>
                <span>Pub/Sub topic path</span>
                <input
                  value={googleChat.topic_id}
                  onChange={(event) => setGoogleChat((current) => ({ ...current, topic_id: event.target.value }))}
                  placeholder="projects/<project_id>/topics/chat-events"
                />
              </label>
              <label>
                <span>Pull subscription path</span>
                <input
                  value={googleChat.subscription_id}
                  onChange={(event) => setGoogleChat((current) => ({ ...current, subscription_id: event.target.value }))}
                  placeholder="projects/<project_id>/subscriptions/chat-events-sub"
                />
              </label>
            </div>

            <p className="settings-section-copy" style={{ marginTop: '-4px' }}>
              Enter the full Pub/Sub resource path for both fields.
            </p>

            <label>
              <span>Service account JSON</span>
              <textarea
                value={googleChat.service_account_json}
                onChange={(event) => setGoogleChat((current) => ({ ...current, service_account_json: event.target.value }))}
                placeholder={googleChat.status.has_service_account_json ? 'Leave blank to keep the currently saved service account JSON' : 'Paste the full Google Cloud service account JSON'}
                rows={10}
              />
            </label>

            <div className="settings-google-chat-actions">
              <p className="settings-section-copy" style={{ margin: 0 }}>
                Changes here are only saved after you click <strong>Save Google Chat</strong>. Saved values are persisted by the backend in the app database.
              </p>
              <button type="button" onClick={saveGoogleChatSettings} disabled={savingGoogleChat || !selectedAgentId}>
                {savingGoogleChat ? 'Saving...' : 'Save Google Chat'}
              </button>
            </div>
          </div>
        </div>
      </section>
    </div>
  )
}

export default Settings
