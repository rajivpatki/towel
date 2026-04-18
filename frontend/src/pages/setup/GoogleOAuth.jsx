import { useState, useMemo } from 'react'
import { useNavigate } from 'react-router-dom'
import { useToast } from '../../components/ToastProvider'

const apiBaseUrl = import.meta.env.VITE_API_BASE_URL || `http://localhost:8000`

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

function GoogleOAuth({ onStatusChange }) {
  const navigate = useNavigate()
  const { showToast } = useToast()
  const [form, setForm] = useState({ client_id: '', client_secret: '' })
  const [busy, setBusy] = useState(false)

  const canSubmit = useMemo(() => {
    return form.client_id.trim().length > 0 && form.client_secret.trim().length > 0
  }, [form])

  async function handleSubmit(event) {
    event.preventDefault()
    setBusy(true)
    try {
      const response = await fetch(`${apiBaseUrl}/api/setup/google/oauth-credentials`, {
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
        title: 'Google OAuth credentials saved',
        description: 'Proceeding to Gmail authorization.'
      })
      setTimeout(() => {
        navigate('/setup/gmail')
      }, 400)
    } catch (err) {
      showToast({
        tone: 'error',
        title: 'Unable to save Google OAuth credentials',
        description: err.message
      })
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="wizard-container">
      <div className="wizard-card">
        <div className="wizard-progress">
          <div className="progress-step active"></div>
          <div className="progress-step"></div>
          <div className="progress-step"></div>
        </div>

        <div>
          <p className="eyebrow">Step 1 of 3</p>
          <h2 style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
            <img src="https://www.google.com/favicon.ico" alt="Google" style={{ width: '24px', height: '24px' }} />
            Google OAuth Credentials
          </h2>
          <p className="hero-copy">
            Create a Google OAuth screen from your own account to access your Gmail.
          </p>
        </div>

        <div className="stack-content">
          <div className="instruction-box">
            <h3>How to get your Client ID and Secret</h3>
            <ol className="instruction-steps">
              <li>
                Open
                {" "}
                <a
                  href="https://console.cloud.google.com/auth/overview/create"
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  Google Configure the OAuth Consent Screen
                </a>
                {" "}
                and configure your app.
              </li>
              <li>
                Next, go to
                {" "}
                <a
                  href="https://console.cloud.google.com/auth/clients/create"
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  Create OAuth client ID
                </a>
                , choose <strong>Desktop app</strong> as the application type, and create the client.
              </li>
              <li>
                Copy the generated <strong>Client ID</strong> and <strong>Client secret</strong> from the
                Google dialog and paste them into the fields below.
              </li>
              <li>
                Enable the
                {" "}
                <a
                  href="https://console.cloud.google.com/apis/library/gmail.googleapis.com"
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  Gmail
                </a>
                {" "}
                and
                {" "}
                <a
                  href="https://console.cloud.google.com/flows/enableapi?apiid=generativelanguage.googleapis.com"
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  Gemini
                </a>
                {" "}
                APIs for the same Google Cloud project.
              </li>
            </ol>
            <p className="note">
              These credentials are stored on your device. Do not share these with anyone.
            </p>
            <p className="note">
              During the Google consent screen in Step 2, approve every requested permission so Gemini and Gmail can both work.
            </p>
          </div>
        </div>

        <form className="stack-form" onSubmit={handleSubmit}>
          <label>
            <span>Client ID</span>
            <input
              value={form.client_id}
              onChange={(e) => setForm((c) => ({ ...c, client_id: e.target.value }))}
              placeholder="Paste your Google OAuth Client ID"
              autoFocus
            />
          </label>
          <label>
            <span>Client Secret</span>
            <input
              type="password"
              value={form.client_secret}
              onChange={(e) => setForm((c) => ({ ...c, client_secret: e.target.value }))}
              placeholder="Paste your Google OAuth Client secret"
            />
          </label>
          <div className="wizard-navigation">
            <button type="submit" disabled={!canSubmit || busy}>
              Continue to Gmail Setup
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

export default GoogleOAuth
