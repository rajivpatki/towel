import { useState, useEffect, useCallback } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
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

function GmailConnect({ onStatusChange }) {
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const { showToast } = useToast()
  const [status, setStatus] = useState(null)
  const [busy, setBusy] = useState(false)

  const loadStatus = useCallback(async () => {
    const response = await fetch(`${apiBaseUrl}/api/setup/status`)
    const data = await parseResponse(response)
    setStatus(data)
    return data
  }, [])

  // Check for OAuth callback results in URL
  useEffect(() => {
    const oauthResult = searchParams.get('oauth')
    const errorMsg = searchParams.get('msg')

    if (oauthResult === 'success') {
      setSearchParams({}, { replace: true })
      loadStatus()
        .then(async (freshStatus) => {
          await onStatusChange?.()
          showToast({
            tone: 'success',
            title: 'Gmail connected',
            description: freshStatus?.google_email || 'Your Google account is ready.'
          })
          setTimeout(() => navigate('/setup/llm'), 400)
        })
        .catch((err) => {
          showToast({
            tone: 'error',
            title: 'Unable to confirm Gmail authorization',
            description: err.message
          })
        })
    } else if (oauthResult === 'error') {
      setSearchParams({}, { replace: true })
      showToast({
        tone: 'error',
        title: 'Gmail authorization failed',
        description: errorMsg || 'Authorization failed'
      })
    }
  }, [searchParams, setSearchParams, showToast, loadStatus, onStatusChange, navigate])

  useEffect(() => {
    loadStatus().catch((err) => {
      showToast({
        tone: 'error',
        title: 'Unable to load Gmail connection status',
        description: err.message
      })
    })
  }, [loadStatus, showToast])

  async function connectGoogle() {
    setBusy(true)
    try {
      const response = await fetch(`${apiBaseUrl}/api/setup/google/connect`, {
        method: 'POST'
      })
      const data = await parseResponse(response)
      showToast({
        tone: 'info',
        title: 'Redirecting to Google',
        description: 'Complete the authorization to continue setup.'
      })
      window.location.href = data.auth_url
    } catch (err) {
      showToast({
        tone: 'error',
        title: 'Unable to start Gmail authorization',
        description: err.message
      })
    } finally {
      setBusy(false)
    }
  }

  function skipToNext() {
    navigate('/setup/llm')
  }

  if (!status) {
    return (
      <div className="wizard-container">
        <div className="wizard-card">Loading...</div>
      </div>
    )
  }

  return (
    <div className="wizard-container">
      <div className="wizard-card">
        <button className="back-button" onClick={() => navigate('/setup/google')} aria-label="Back">
          <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
            <path d="M15 18l-6-6 6-6"/>
          </svg>
        </button>

        <div className="wizard-progress">
          <div className="progress-step completed"></div>
          <div className="progress-step active"></div>
          <div className="progress-step"></div>
        </div>

        <div>
          <p className="eyebrow">Step 2 of 3</p>
          <h2>Connect Gmail Account</h2>
        </div>

        <div className="stack-content">
          <div className="google-login-container">
            <p className="google-login-hint">Sign in with your Google account</p>
            <button
              className="google-one-tap-btn"
              onClick={connectGoogle}
              disabled={busy}
            >
              <div className="google-icon">
                <svg viewBox="0 0 24 24" width="18" height="18">
                  <path fill="#4285F4" d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92c-.26 1.37-1.04 2.53-2.21 3.31v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.09z"/>
                  <path fill="#34A853" d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z"/>
                  <path fill="#FBBC05" d="M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.07H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.93l2.85-2.22.81-.62z"/>
                  <path fill="#EA4335" d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.07l3.66 2.84c.87-2.6 3.3-4.53 6.16-4.53z"/>
                </svg>
              </div>
              <span>{busy ? 'Connecting…' : 'Continue with Google'}</span>
            </button>
          </div>

          <div className="instruction-box">
            <h3>Before connecting</h3>
            <p>
              If your OAuth app is in testing mode, add your Gmail address as a test user:
            </p>
            <ol className="instruction-steps">
              <li>Visit <a href="https://console.cloud.google.com/apis/credentials/consent" target="_blank" rel="noopener noreferrer">OAuth consent screen</a></li>
              <li>Scroll to <strong>Test users</strong> section</li>
              <li>Click <strong>Add Users</strong></li>
              <li>Enter your Gmail address</li>
              <li>Click <strong>Save</strong></li>
            </ol>
          </div>

          {status.google_account_connected ? (
            <div className="wizard-navigation single">
              <button onClick={skipToNext}>Continue</button>
            </div>
          ) : null}
        </div>
      </div>
    </div>
  )
}

export default GmailConnect
