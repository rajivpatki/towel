import { useState, useEffect } from 'react'
import { useToast } from '../components/ToastProvider'

const apiBaseUrl = import.meta.env.VITE_API_BASE_URL || `${window.location.protocol}//${window.location.hostname}:8000`

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
      showToast({
        tone: 'success',
        title: 'Preferences saved',
        description: 'Your organization rules are ready to use.'
      })
    } catch (err) {
      showToast({
        tone: 'error',
        title: 'Unable to save preferences',
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
        <h2>Preferences</h2>
        <p style={{ color: 'var(--color-text-secondary)' }}>Loading...</p>
      </div>
    )
  }

  return (
    <div className="preferences-container">
      <div style={{ marginBottom: '2rem' }}>
        <p className="eyebrow">Settings</p>
        <h2>Email Preferences</h2>
        <p style={{ color: 'var(--color-text-secondary)' }}>
          Define your email organization rules in natural language
        </p>
      </div>

      <div style={{ marginBottom: '1.5rem' }}>
        {preferences.length === 0 ? (
          <div className="info-box" style={{ textAlign: 'center', padding: '2rem' }}>
            <p style={{ color: 'var(--color-text-secondary)' }}>
              No preferences set yet. Add your first preference below.
            </p>
          </div>
        ) : (
          preferences.map((pref, idx) => (
            <div key={pref.id} className="preference-item">
              <label className="preference-label">
                Preference {idx + 1}
              </label>
              <textarea
                className="preference-input"
                value={pref.value}
                onChange={(e) => updatePreference(pref.id, 'value', e.target.value)}
                placeholder="Example: Move all emails from MyBank to Finances/Personal/Banking"
              />
              <div className="preference-hint">
                Describe your email rule in plain English
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
          Add Preference
        </button>
        <button
          type="button"
          className="secondary"
          onClick={savePreferences}
          disabled={saving || preferences.length === 0}
        >
          {saving ? 'Saving...' : 'Save All'}
        </button>
      </div>

      <div className="instruction-box" style={{ marginTop: '2rem' }}>
        <h3>How it works</h3>
        <p>
          Define your email organization rules in natural language. Towel will use these preferences
          to automatically organize your emails and create appropriate filters.
        </p>
        <p style={{ marginBottom: 0 }}>
          <strong>Examples:</strong>
        </p>
        <ul style={{ marginTop: '0.5rem', paddingLeft: '1.5rem' }}>
          <li>Move all emails from @company.com to Work/Company folder</li>
          <li>Tag all newsletters with Newsletter label</li>
          <li>Archive all promotional emails from specific senders</li>
        </ul>
      </div>
    </div>
  )
}

export default Preferences
