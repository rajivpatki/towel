import { useState, useEffect, useMemo } from 'react'
import { useNavigate } from 'react-router-dom'
import { useToast } from '../../components/ToastProvider'

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

function LLMConfig({ onStatusChange }) {
  const navigate = useNavigate()
  const { showToast } = useToast()
  const [status, setStatus] = useState(null)
  const [form, setForm] = useState({ agent_id: 'openai:gpt-5.4', api_key: '' })
  const [busy, setBusy] = useState(false)

  const canSubmit = useMemo(() => {
    return form.agent_id.trim().length > 0 && form.api_key.trim().length > 0
  }, [form])

  async function loadStatus() {
    const response = await fetch(`${apiBaseUrl}/api/setup/status`)
    const data = await parseResponse(response)
    setStatus(data)
    setForm((current) => ({
      ...current,
      agent_id: data.selected_agent_id || current.agent_id
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
    setBusy(true)
    try {
      const response = await fetch(`${apiBaseUrl}/api/setup/llm`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json'
        },
        body: JSON.stringify(form)
      })
      await parseResponse(response)
      await onStatusChange?.()
      showToast({
        tone: 'success',
        title: 'AI agent configured',
        description: 'Setup is complete. Opening your workspace.'
      })
      setTimeout(() => {
        navigate('/chat')
      }, 400)
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

  if (!status) {
    return (
      <div className="wizard-container">
        <div className="wizard-card">Loading...</div>
      </div>
    )
  }

  const selectedAgent = status.available_agents.find((agent) => agent.agent_id === form.agent_id)

  return (
    <div className="wizard-container">
      <div className="wizard-card">
        <div className="wizard-progress">
          <div className="progress-step completed"></div>
          <div className="progress-step completed"></div>
          <div className="progress-step active"></div>
        </div>

        <div>
          <p className="eyebrow">Step 3 of 3</p>
          <h2>Configure AI Agent</h2>
          <p className="hero-copy">
            Select your preferred AI model and provide the API key.
          </p>
        </div>

        <form className="stack-form" onSubmit={handleSubmit}>
          <label>
            <span>AI Agent</span>
            <select
              value={form.agent_id}
              onChange={(e) => setForm((c) => ({ ...c, agent_id: e.target.value }))}
            >
              {status.available_agents.map((agent) => (
                <option value={agent.agent_id} key={agent.agent_id}>
                  {agent.label}
                </option>
              ))}
            </select>
          </label>

          <label>
            <span>API Key</span>
            <input
              type="password"
              value={form.api_key}
              onChange={(e) => setForm((c) => ({ ...c, api_key: e.target.value }))}
              placeholder="Paste your API key"
            />
          </label>

          <div className="agent-card">
            <div>
              <strong>{selectedAgent?.label}</strong>
              <p>{selectedAgent?.provider} · {selectedAgent?.model}</p>
            </div>
            <div className="agent-badges">
              <StatusPill label={`Mode: ${selectedAgent?.reasoning_mode || 'n/a'}`} ok />
              <StatusPill label={`Verbosity: ${selectedAgent?.verbosity || 'n/a'}`} ok />
            </div>
          </div>

          <div className="wizard-navigation">
            <button type="button" className="secondary" onClick={() => navigate('/setup/gmail')}>
              Back
            </button>
            <button type="submit" disabled={!canSubmit || busy}>
              Complete Setup
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

export default LLMConfig
