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

function formatTimestamp(timestamp) {
  const date = new Date(timestamp)
  return new Intl.DateTimeFormat('en-US', {
    dateStyle: 'medium',
    timeStyle: 'short'
  }).format(date)
}

function History() {
  const { showToast } = useToast()
  const [history, setHistory] = useState([])
  const [loading, setLoading] = useState(true)

  async function loadHistory() {
    try {
      const response = await fetch(`${apiBaseUrl}/api/history`)
      const data = await parseResponse(response)
      setHistory(data.items || [])
    } catch (err) {
      showToast({
        tone: 'error',
        title: 'Unable to load action history',
        description: err.message
      })
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    loadHistory()
  }, [])

  if (loading) {
    return (
      <div>
        <h2>Action History</h2>
        <p style={{ color: 'var(--color-text-secondary)' }}>Loading...</p>
      </div>
    )
  }

  return (
    <div>
      <div style={{ marginBottom: '2rem' }}>
        <p className="eyebrow">Activity Log</p>
        <h2>Action History</h2>
        <p style={{ color: 'var(--color-text-secondary)' }}>
          Review all actions taken by Towel on your Gmail account
        </p>
      </div>

      {history.length === 0 ? (
        <div className="info-box" style={{ textAlign: 'center', padding: '3rem 2rem' }}>
          <p style={{ fontSize: '1.125rem', color: 'var(--color-text-secondary)' }}>
            No actions recorded yet
          </p>
          <p style={{ fontSize: '0.9375rem', color: 'var(--color-text-tertiary)', marginTop: '0.5rem' }}>
            Actions will appear here as you use the chat interface
          </p>
        </div>
      ) : (
        <div className="history-list">
          {history.map((item) => (
            <div key={item.id} className="history-item">
              <div className="history-header">
                <div className="history-action">{item.action}</div>
                <div className="history-timestamp">{formatTimestamp(item.timestamp)}</div>
              </div>
              <div className="history-details">{item.details}</div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

export default History
