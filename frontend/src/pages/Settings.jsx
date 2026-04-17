import { useEffect, useMemo, useState } from 'react'
import { useToast } from '../components/ToastProvider'
import CustomSelect from '../components/CustomSelect'

const apiBaseUrl = import.meta.env.VITE_API_BASE_URL || `http://localhost:8000`

async function parseResponse(response) {
  if (response.ok) {
    if (response.status === 204) {
      return null
    }
    return response.json()
  }

  // Handle unauthorized - redirect to login/setup
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

function newAgent() {
  return {
    agent_id: `custom-${Date.now()}-${Math.random().toString(16).slice(2, 8)}`,
    provider: '',
    auth_mode: 'api_key',
    label: '',
    model: '',
    reasoning_mode: 'standard',
    verbosity: 'medium',
    base_url: '',
    is_custom: true
  }
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
  const [saving, setSaving] = useState(false)
  const [selectedAgentId, setSelectedAgentId] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [hasApiKey, setHasApiKey] = useState(false)
  const [agents, setAgents] = useState([])

  const customAgents = useMemo(() => agents.filter((agent) => agent.is_custom), [agents])
  const selectedAgent = useMemo(() => agents.find((agent) => agent.agent_id === selectedAgentId) || null, [agents, selectedAgentId])
  const selectedUsesAPIKey = selectedAgent ? selectedAgent.auth_mode !== 'google_oauth' : true

  async function loadSettings() {
    try {
      const response = await fetch(`${apiBaseUrl}/api/settings`, {
        credentials: 'include'
      })
      const data = await parseResponse(response)
      setSelectedAgentId(data.selected_agent_id || data.agents?.[0]?.agent_id || '')
      setHasApiKey(Boolean(data.has_api_key))
      setAgents(Array.isArray(data.agents) ? data.agents.map(normalizeAgent) : [])
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

  function updateCustomAgent(agentId, field, value) {
    setAgents((current) => current.map((agent) => (
      agent.agent_id === agentId ? { ...agent, [field]: value } : agent
    )))
  }

  function addCustomAgent() {
    const item = newAgent()
    setAgents((current) => [...current, item])
    setSelectedAgentId(item.agent_id)
  }

  function removeCustomAgent(agentId) {
    setAgents((current) => {
      const nextAgents = current.filter((agent) => agent.agent_id !== agentId)
      if (selectedAgentId === agentId) {
        const fallback = nextAgents.find((agent) => !agent.is_custom) || nextAgents[0]
        setSelectedAgentId(fallback?.agent_id || '')
      }
      return nextAgents
    })
  }

  async function saveSettings() {
    setSaving(true)
    try {
      const response = await fetch(`${apiBaseUrl}/api/settings`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json'
        },
        credentials: 'include',
        body: JSON.stringify({
          selected_agent_id: selectedAgentId || null,
          api_key: apiKey,
          agents: customAgents.map((agent) => ({
            agent_id: agent.agent_id,
            provider: agent.provider,
            auth_mode: agent.auth_mode,
            label: agent.label,
            model: agent.model,
            reasoning_mode: agent.reasoning_mode,
            verbosity: agent.verbosity,
            base_url: agent.base_url
          }))
        })
      })
      await parseResponse(response)
      setApiKey('')
      await loadSettings()
      showToast({
        tone: 'success',
        title: 'Settings saved',
        description: selectedUsesAPIKey
          ? 'Your model configuration and API key were updated.'
          : 'Your model configuration was updated. Gemini continues to use your Google OAuth connection.'
      })
    } catch (err) {
      showToast({
        tone: 'error',
        title: 'Unable to save settings',
        description: err.message
      })
    } finally {
      setSaving(false)
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
          Manage your active model, rotate API credentials, and add custom providers while keeping the same visual language as the rest of Towel.
        </p>
      </div>

      <div className="settings-grid">
        <section className="panel settings-section">
          <div className="panel-header settings-panel-header">
            <div>
              <h3>Active model</h3>
              <p className="settings-section-copy">Choose which configured model powers chat responses.</p>
            </div>
          </div>

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
                <p>{selectedAgent.provider} · {selectedAgent.model}</p>
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
        </section>

        <section className="panel settings-section">
          <div className="panel-header settings-panel-header">
            <div>
              <h3>API key</h3>
              <p className="settings-section-copy">Update the API key used for the currently selected model.</p>
            </div>
            {hasApiKey ? <span className="status-pill ok">Configured</span> : <span className="status-pill pending">Missing</span>}
          </div>

          <label>
            <span>New API key</span>
            <input
              type="password"
              value={apiKey}
              onChange={(event) => setApiKey(event.target.value)}
              placeholder={hasApiKey ? 'Leave blank to keep existing key' : 'Paste your API key'}
            />
          </label>
        </section>
      </div>

      <section className="panel settings-section settings-custom-section">
        <div className="panel-header settings-panel-header">
          <div>
            <h3>Custom models</h3>
            <p className="settings-section-copy">Add provider-compatible chat models by specifying an agent id, model name, and base URL.</p>
          </div>
          <button type="button" onClick={addCustomAgent}>Add model</button>
        </div>

        {customAgents.length === 0 ? (
          <div className="info-box settings-empty-box">
            <p>No custom models yet. Add one to connect a different provider or endpoint.</p>
          </div>
        ) : (
          <div className="settings-custom-list">
            {customAgents.map((agent, index) => (
              <div key={agent.agent_id} className="settings-model-card">
                <div className="settings-model-card-header">
                  <div>
                    <strong>Custom model {index + 1}</strong>
                    <p>Stored locally and merged with the built-in models list.</p>
                  </div>
                  <button type="button" className="secondary" onClick={() => removeCustomAgent(agent.agent_id)}>
                    Remove
                  </button>
                </div>

                <div className="settings-model-grid">
                  <label>
                    <span>Agent ID</span>
                    <input value={agent.agent_id} onChange={(event) => updateCustomAgent(agent.agent_id, 'agent_id', event.target.value)} placeholder="provider:model-id" />
                  </label>
                  <label>
                    <span>Provider</span>
                    <input value={agent.provider} onChange={(event) => updateCustomAgent(agent.agent_id, 'provider', event.target.value)} placeholder="openai-compatible" />
                  </label>
                  <label>
                    <span>Label</span>
                    <input value={agent.label} onChange={(event) => updateCustomAgent(agent.agent_id, 'label', event.target.value)} placeholder="My Custom Model" />
                  </label>
                  <label>
                    <span>Model</span>
                    <input value={agent.model} onChange={(event) => updateCustomAgent(agent.agent_id, 'model', event.target.value)} placeholder="gpt-4.1-mini" />
                  </label>
                  <label>
                    <span>Reasoning mode</span>
                    <CustomSelect
                      value={agent.reasoning_mode}
                      onChange={(value) => updateCustomAgent(agent.agent_id, 'reasoning_mode', value)}
                      options={[
                        { value: 'standard', label: 'standard' },
                        { value: 'thinking', label: 'thinking' }
                      ]}
                      placeholder="Select reasoning mode..."
                      label="Reasoning mode"
                    />
                  </label>
                  <label>
                    <span>Verbosity</span>
                    <CustomSelect
                      value={agent.verbosity}
                      onChange={(value) => updateCustomAgent(agent.agent_id, 'verbosity', value)}
                      options={[
                        { value: 'low', label: 'low' },
                        { value: 'medium', label: 'medium' },
                        { value: 'high', label: 'high' }
                      ]}
                      placeholder="Select verbosity..."
                      label="Verbosity"
                    />
                  </label>
                </div>

                <label>
                  <span>Base URL</span>
                  <input value={agent.base_url} onChange={(event) => updateCustomAgent(agent.agent_id, 'base_url', event.target.value)} placeholder="https://api.example.com/v1" />
                </label>
              </div>
            ))}
          </div>
        )}
      </section>

      <div className="wizard-navigation settings-actions">
        <button type="button" className="secondary" onClick={() => {
          setApiKey('')
          loadSettings()
        }} disabled={saving}>
          Reset
        </button>
        <button type="button" onClick={saveSettings} disabled={saving || !selectedAgentId}>
          {saving ? 'Saving...' : 'Save settings'}
        </button>
      </div>
    </div>
  )
}

export default Settings
