import { useState, useEffect } from 'react'
import { useToast } from '../components/ToastProvider'

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

function Preferences() {
  const { showToast } = useToast()
  const [preferences, setPreferences] = useState([])
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  async function loadPreferences() {
    try {
      const response = await fetch(`${apiBaseUrl}/api/preferences`)
      const data = await parseResponse(response)
      setPreferences(data.preferences || [])
    } catch (err) {
      showToast({
        tone: 'error',
        title: 'Unable to load preferences',
        description: err.message
      })
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    loadPreferences()
  }, [])

  async function savePreferences() {
    setSaving(true)
    try {
      const response = await fetch(`${apiBaseUrl}/api/preferences`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json'
        },
        body: JSON.stringify({ preferences })
      })
      await parseResponse(response)
      await loadPreferences()
      showToast({
        tone: 'success',
        title: 'Personalisation saved',
        description: 'Your instructions are now included in the agent behavior.'
      })
    } catch (err) {
      showToast({
        tone: 'error',
        title: 'Unable to save personalisation',
        description: err.message
      })
    } finally {
      setSaving(false)
    }
  }

  function addPreference() {
    setPreferences([...preferences, { id: Date.now(), label: '', value: '' }])
  }

  function updatePreference(id, field, value) {
    setPreferences(preferences.map((pref) =>
      pref.id === id ? { ...pref, [field]: value } : pref
    ))
  }

  function removePreference(id) {
    setPreferences(preferences.filter((pref) => pref.id !== id))
  }

  if (loading) {
    return (
      <div className="preferences-container">
        <h2>Personalise</h2>
        <p style={{ color: 'var(--color-text-secondary)' }}>Loading...</p>
      </div>
    )
  }

  return (
    <div className="preferences-container">
      <div style={{ marginBottom: '2rem' }}>
        <p className="eyebrow">Personalise</p>
        <h2>Personalise your agent</h2>
        <p style={{ color: 'var(--color-text-secondary)' }}>
          Add one or more natural-language instructions. Each item is stored separately and added to the system prompt.
        </p>
      </div>

      <div style={{ marginBottom: '1.5rem' }}>
        {preferences.length === 0 ? (
          <div className="info-box" style={{ textAlign: 'center', padding: '2rem' }}>
            <p style={{ color: 'var(--color-text-secondary)' }}>
              No personalisations yet. Add your first instruction below.
            </p>
          </div>
        ) : (
          preferences.map((pref, idx) => (
            <div key={pref.id} className="preference-item">
              <label className="preference-label">
                Instruction {idx + 1}
              </label>
              <textarea
                className="preference-input"
                value={pref.value}
                onChange={(e) => updatePreference(pref.id, 'value', e.target.value)}
                placeholder="Example: Prefer concise answers with a short summary first, then actionable steps."
              />
              <div className="preference-hint">
                Each item can contain one or more sentences.
              </div>
              <div style={{ marginTop: '1rem' }}>
                <button
                  type="button"
                  className="secondary"
                  onClick={() => removePreference(pref.id)}
                  style={{ padding: '0.5rem 1rem', fontSize: '0.875rem' }}
                >
                  Remove
                </button>
              </div>
            </div>
          ))
        )}
      </div>

      <div style={{ display: 'flex', gap: '1rem' }}>
        <button type="button" onClick={addPreference}>
          Add +
        </button>
        <button
          type="button"
          className="secondary"
          onClick={savePreferences}
          disabled={saving || preferences.length === 0}
        >
          {saving ? 'Saving...' : 'Save'}
        </button>
      </div>

      <div className="instruction-box" style={{ marginTop: '2rem' }}>
        <h3>How personalisation works</h3>
        <p>
          Each saved item is appended to the system prompt under <strong>PERSONALISED USER INSTRUCTIONS</strong>
          as a separate bullet point.
        </p>
        <p style={{ marginBottom: 0 }}>
          <strong>Examples:</strong>
        </p>
        <ul style={{ marginTop: '0.5rem', paddingLeft: '1.5rem' }}>
          <li>Prefer bullet lists over long paragraphs.</li>
          <li>When suggesting cleanup, prioritize reversible actions first.</li>
          <li>Use tables when comparing multiple label or filter options.</li>
        </ul>
      </div>
    </div>
  )
}

export default Preferences
