import { useState, useEffect, useMemo } from 'react'
import { useNavigate } from 'react-router-dom'
import { useToast } from '../../components/ToastProvider'
import CustomSelect from '../../components/CustomSelect'

const apiBaseUrl = import.meta.env.VITE_API_BASE_URL || `http://127.0.0.1:8000`

async function parseResponse(response) {
  if (response.ok) {
    if (response.status === 204) {
      return null
    }
    return response.json()
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

function StatusPill({ label, ok }) {
  return <span className={`status-pill ${ok ? 'ok' : 'pending'}`}>{label}</span>
}

function SparkleIcon() {
  return (
    <svg className="sparkle" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9L12 3z" />
    </svg>
  )
}

function LLMConfig({ onStatusChange }) {
  const navigate = useNavigate()
  const { showToast } = useToast()
  const [status, setStatus] = useState(null)
  const [form, setForm] = useState({ agent_id: 'openai:gpt-5.4', api_key: '' })
  const [busy, setBusy] = useState(false)
  const [geminiBusy, setGeminiBusy] = useState(false)
  const [geminiEnableUrl, setGeminiEnableUrl] = useState('')
  const [geminiNeedsReauth, setGeminiNeedsReauth] = useState(false)
  const [geminiDetail, setGeminiDetail] = useState('')

  const availableAgents = status?.available_agents || []
  const geminiAgent = useMemo(() => availableAgents.find((agent) => agent.auth_mode === 'google_oauth' || agent.provider === 'gemini') || null, [availableAgents])
  const apiAgents = useMemo(() => availableAgents.filter((agent) => agent.auth_mode !== 'google_oauth'), [availableAgents])
  const selectedAgent = useMemo(() => apiAgents.find((agent) => agent.agent_id === form.agent_id) || apiAgents[0] || null, [apiAgents, form.agent_id])

  const canSubmit = useMemo(() => {
    return Boolean(selectedAgent?.agent_id) && form.api_key.trim().length > 0
  }, [form.api_key, selectedAgent])

  async function completeSetup() {
    await onStatusChange?.()
    showToast({
      tone: 'success',
      title: 'AI agent configured',
      description: 'Setup is complete. Opening your workspace.'
    })
    setTimeout(() => {
      navigate('/chat')
    }, 400)
  }

  async function loadStatus() {
    const response = await fetch(`${apiBaseUrl}/api/setup/status`)
    const data = await parseResponse(response)
    setStatus(data)

    const builtInApiAgents = (data.available_agents || []).filter((agent) => agent.auth_mode !== 'google_oauth')
    const selected = (data.available_agents || []).find((agent) => agent.agent_id === data.selected_agent_id)

    setForm((current) => ({
      ...current,
      agent_id: selected?.auth_mode === 'api_key'
        ? selected.agent_id
        : builtInApiAgents[0]?.agent_id || current.agent_id
    }))
  }

  useEffect(() => {
    loadStatus().catch((err) => {
      showToast({
        tone: 'error',
        title: 'Unable to load AI agent configuration',
        description: err.message
      })
    })
  }, [showToast])

  async function handleSubmit(event) {
    event.preventDefault()
    if (!selectedAgent) {
      return
    }
    setBusy(true)
    try {
      const response = await fetch(`${apiBaseUrl}/api/setup/llm`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json'
        },
        body: JSON.stringify({
          agent_id: selectedAgent.agent_id,
          api_key: form.api_key
        })
      })
      await parseResponse(response)
      await completeSetup()
    } catch (err) {
      showToast({
        tone: 'error',
        title: 'Unable to save AI agent configuration',
        description: err.message
      })
    } finally {
      setBusy(false)
    }
  }

  async function handleUseGemini() {
    if (!geminiAgent) {
      return
    }
    setGeminiBusy(true)
    setGeminiEnableUrl('')
    setGeminiNeedsReauth(false)
    setGeminiDetail('')
    try {
      const response = await fetch(`${apiBaseUrl}/api/setup/llm/gemini`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json'
        },
        body: JSON.stringify({
          agent_id: geminiAgent.agent_id
        })
      })
      const data = await parseResponse(response)
      if (data.status === 'api_disabled') {
        setGeminiEnableUrl(data.enable_url || 'https://console.cloud.google.com/flows/enableapi?apiid=generativelanguage.googleapis.com')
        setGeminiDetail(data.detail || 'Enable the Google Generative Language API to continue with Gemini.')
        showToast({
          tone: 'info',
          title: 'Enable Gemini API first',
          description: data.detail || 'The Google Generative Language API is not enabled for this project yet.'
        })
        return
      }
      if (data.status === 'needs_reauth') {
        setGeminiNeedsReauth(true)
        setGeminiDetail(data.detail || 'Insufficient scopes. Reconnect your Google account.')
        showToast({
          tone: 'info',
          title: 'Reconnect Google Account',
          description: data.detail || 'Please reconnect your Google account and ensure you grant all requested permissions.'
        })
        return
      }
      if (!data.success) {
        throw new Error(data.detail || 'Unable to activate Gemini')
      }
      await loadStatus()
      await completeSetup()
    } catch (err) {
      showToast({
        tone: 'error',
        title: 'Unable to activate Gemini',
        description: err.message
      })
    } finally {
      setGeminiBusy(false)
    }
  }

  if (!status) {
    return (
      <div className="wizard-container">
        <div className="wizard-card">Loading...</div>
      </div>
    )
  }

  return (
    <div className="wizard-container wizard-llm-container">
      <div className="wizard-card wizard-llm-card">
        <div className="wizard-progress">
          <div className="progress-step completed"></div>
          <div className="progress-step completed"></div>
          <div className="progress-step active"></div>
        </div>

        <div>
          <p className="eyebrow">Step 3 of 3</p>
          <h2>Configure AI Agent</h2>
          <p className="hero-copy">
            Use Gemini with your connected Google account or keep an API-key flow for OpenAI-compatible models.
          </p>
        </div>

        <div className="llm-choice-stack">
          <section className="llm-option-card">
            <div className="llm-option-header">
              <div>
                <h3>Use Gemini</h3>
                <p className="llm-option-copy">Use your Google account for Gemini instead of managing another API key.</p>
              </div>
              {status.selected_agent_id === geminiAgent?.agent_id ? <StatusPill label="Current model" ok /> : null}
            </div>

            <div className="gemini-actions">
              <button type="button" className="gemini-btn" onClick={handleUseGemini} disabled={!geminiAgent || !status.google_account_connected || geminiBusy}>
                <SparkleIcon />
                <span>{geminiBusy ? 'Trying...' : 'Use Gemini'}</span>
              </button>

              {geminiEnableUrl ? (
                <a className="gemini-enable-btn" href={geminiEnableUrl} target="_blank" rel="noopener noreferrer">
                  Enable the API
                </a>
              ) : null}

              {geminiNeedsReauth ? (
                <button type="button" className="gemini-enable-btn" onClick={() => navigate('/setup/google')}>
                  Reconnect Account
                </button>
              ) : null}
            </div>

            {(geminiEnableUrl || geminiNeedsReauth) && geminiDetail ? <p className="llm-helper-text">{geminiDetail}</p> : null}

            {!geminiEnableUrl && !status.google_account_connected ? (
              <p className="llm-section-note warning">Complete Step 2 first so Towel can authenticate Gemini with your Google account.</p>
            ) : null}
          </section>

          <div className="llm-or-divider">OR</div>

          <section className="llm-option-card">
            <div className="llm-option-header">
              <div>
                <h3>Use an API key</h3>
                <p className="llm-option-copy">Keep using OpenAI or DeepSeek with the provider key you already manage.</p>
              </div>
            </div>

            <form className="stack-form" onSubmit={handleSubmit}>
              <label>
                <span>AI Agent</span>
                <CustomSelect
                  value={selectedAgent?.agent_id || ''}
                  onChange={(value) => setForm((current) => ({ ...current, agent_id: value }))}
                  options={apiAgents.map((agent) => ({
                    value: agent.agent_id,
                    label: agent.label
                  }))}
                  placeholder="Select AI Agent..."
                  label="AI Agent"
                />
              </label>

              <label>
                <span>API Key</span>
                <input
                  type="password"
                  value={form.api_key}
                  onChange={(event) => setForm((current) => ({ ...current, api_key: event.target.value }))}
                  placeholder="Paste your API key"
                />
              </label>

              <div className="wizard-navigation llm-api-navigation">
                <button type="submit" disabled={!canSubmit || busy || !selectedAgent}>
                  {busy ? 'Saving...' : 'Complete Setup'}
                </button>
              </div>
            </form>
          </section>
        </div>
      </div>
    </div>
  )
}

export default LLMConfig
