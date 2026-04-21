import { useEffect, useMemo, useState } from 'react'
import { useToast } from '../components/ToastProvider'

const apiBaseUrl = import.meta.env.VITE_API_BASE_URL || `http://localhost:8000`
const defaultSQL = `SELECT message_id, subject, from_email, internal_date, is_unread
FROM synced_emails
WHERE is_deleted = 0
ORDER BY internal_date_unix DESC
LIMIT 25`

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

function formatTimestamp(value) {
  if (!value) {
    return '—'
  }
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return value
  }
  return new Intl.DateTimeFormat('en-US', {
    dateStyle: 'medium',
    timeStyle: 'short'
  }).format(date)
}

function formatCellValue(value) {
  if (value === null || value === undefined || value === '') {
    return '—'
  }
  if (typeof value === 'boolean') {
    return value ? 'true' : 'false'
  }
  if (typeof value === 'object') {
    return JSON.stringify(value)
  }
  return String(value)
}

function downloadTextFile(filename, content, mimeType) {
  const blob = new Blob([content], { type: mimeType })
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = filename
  document.body.appendChild(anchor)
  anchor.click()
  document.body.removeChild(anchor)
  URL.revokeObjectURL(url)
}

function escapeCSVValue(value) {
  const text = formatCellValue(value)
  if (text === 'â€”') {
    return ''
  }
  if (/[",\n]/.test(text)) {
    return `"${text.replaceAll('"', '""')}"`
  }
  return text
}

function Development({ initialSyncStatus, onStatusChange }) {
  const { showToast } = useToast()
  const [syncStatus, setSyncStatus] = useState(initialSyncStatus || null)
  const [loadingStatus, setLoadingStatus] = useState(!initialSyncStatus)
  const [sql, setSQL] = useState(defaultSQL)
  const [runningQuery, setRunningQuery] = useState(false)
  const [triggeringMode, setTriggeringMode] = useState('')
  const [queryResult, setQueryResult] = useState(null)

  const syncMetaItems = useMemo(() => {
    if (!syncStatus) {
      return []
    }
    return [
      { label: 'Last sync', value: formatTimestamp(syncStatus.last_successful_sync_at) },
      { label: 'Newest mail', value: formatTimestamp(syncStatus.newest_message_at) },
      { label: 'Oldest mail', value: formatTimestamp(syncStatus.oldest_message_at) },
      { label: 'History cursor', value: syncStatus.sync_cursor_history_id ? 'Present' : 'Not set' },
      { label: 'Embeddings', value: `${syncStatus.embedding_count || 0} indexed` },
      { label: 'Embedding model', value: syncStatus.embedding_model || 'Unavailable' }
    ]
  }, [syncStatus])

  const syncSummaryItems = useMemo(() => {
    if (!syncStatus) {
      return []
    }
    const embeddingsLabel = syncStatus.embedding_model
      ? `${syncStatus.embedding_count || 0} ${syncStatus.embedding_model}`
      : `${syncStatus.embedding_count || 0} indexed`
    return [
      { label: 'Mailbox', value: syncStatus.mailbox_email || 'Not connected' },
      { label: 'Window', value: `${syncStatus.synced_window_days || 0} days` },
      { label: 'Messages', value: `${syncStatus.message_count || 0}` },
      { label: 'Embeddings', value: embeddingsLabel }
    ]
  }, [syncStatus])

  async function loadStatus(showSuccessToast = false) {
    setLoadingStatus(true)
    try {
      const response = await fetch(`${apiBaseUrl}/api/development/email-sync/status`, {
        credentials: 'include'
      })
      const data = await parseResponse(response)
      setSyncStatus(data)
      if (showSuccessToast) {
        showToast({
          tone: 'success',
          title: 'Sync status refreshed',
          description: 'The latest cache metadata was loaded.'
        })
      }
      if (typeof onStatusChange === 'function') {
        await onStatusChange()
      }
      return data
    } catch (err) {
      showToast({
        tone: 'error',
        title: 'Unable to load sync status',
        description: err.message
      })
      return null
    } finally {
      setLoadingStatus(false)
    }
  }

  useEffect(() => {
    if (!initialSyncStatus) {
      loadStatus()
    }
  }, [])

  useEffect(() => {
	if (initialSyncStatus) {
	  setSyncStatus(initialSyncStatus)
	}
  }, [initialSyncStatus])

  async function runQuery() {
    setRunningQuery(true)
    try {
      const response = await fetch(`${apiBaseUrl}/api/development/email-sync/query`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json'
        },
        credentials: 'include',
        body: JSON.stringify({ sql })
      })
      const data = await parseResponse(response)
      setQueryResult(data)
      setSyncStatus(data.sync)
    } catch (err) {
      showToast({
        tone: 'error',
        title: 'Query failed',
        description: err.message
      })
    } finally {
      setRunningQuery(false)
    }
  }

  async function triggerSync(mode) {
    setTriggeringMode(mode)
    try {
      const response = await fetch(`${apiBaseUrl}/api/development/email-sync/trigger`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json'
        },
        credentials: 'include',
        body: JSON.stringify({ mode })
      })
      await parseResponse(response)
      showToast({
        tone: 'success',
        title: `${mode === 'full' ? 'Cache rebuild' : 'Partial'} sync started`,
        description: 'Refresh the sync status in a few moments to see updated progress.'
      })
      await loadStatus()
    } catch (err) {
      showToast({
        tone: 'error',
        title: 'Unable to trigger sync',
        description: err.message
      })
    } finally {
      setTriggeringMode('')
    }
  }

  function downloadQueryAsCSV() {
    if (!queryResult || !Array.isArray(queryResult.columns) || !Array.isArray(queryResult.rows)) {
      return
    }
    const header = queryResult.columns.map(escapeCSVValue).join(',')
    const rows = queryResult.rows.map((row) =>
      queryResult.columns.map((column) => escapeCSVValue(row[column])).join(',')
    )
    const csv = [header, ...rows].join('\n')
    downloadTextFile('sql-results.csv', csv, 'text/csv;charset=utf-8')
  }

  return (
    <div className="development-page">
      <div className="development-hero">
        <p className="eyebrow">Development</p>
        <h2>Email cache inspection</h2>
        <p className="development-copy">
          Inspect the SQLite Gmail cache, monitor incremental sync freshness, and run safe read-only SQL against the synced email tables.
        </p>
      </div>

      <section className="panel development-section">
        <div className="panel-header development-panel-header">
          <div>
            <h3>Sync status</h3>
            <p className="development-section-copy">The cache starts with a recent-window sync and then advances with Gmail history cursors.</p>
          </div>
          <div className="development-status-actions">
            <button type="button" className="secondary" onClick={() => loadStatus(true)} disabled={loadingStatus || triggeringMode !== ''}>
              {loadingStatus ? 'Refreshing...' : 'Refresh status'}
            </button>
            <button type="button" className="secondary" onClick={() => triggerSync('partial')} disabled={triggeringMode !== '' || loadingStatus}>
              {triggeringMode === 'partial' ? 'Starting...' : 'Run partial sync'}
            </button>
            <button type="button" onClick={() => triggerSync('full')} disabled={triggeringMode !== '' || loadingStatus}>
              {triggeringMode === 'full' ? 'Starting...' : 'Rebuild cache'}
            </button>
          </div>
        </div>

        <div className="development-status-strip">
          <span className={`status-pill${syncStatus?.sync_status === 'running' ? ' pending' : ' ok'}`}>
            {syncStatus?.sync_status || 'idle'}
          </span>
          {syncSummaryItems.map((item) => (
            <span key={item.label} className="status-pill neutral">
              <strong>{item.label}:</strong>&nbsp;{item.value}
            </span>
          ))}
        </div>

        {syncStatus?.last_sync_error ? (
          <div className="alert error" style={{ marginTop: '1rem' }}>
            {syncStatus.last_sync_error}
          </div>
        ) : null}

        <div className="development-meta-grid">
          {syncMetaItems.map((item) => (
            <div key={item.label} className="development-meta-card">
              <span>{item.label}</span>
              <strong>{item.value}</strong>
            </div>
          ))}
        </div>
      </section>

      <section className="panel development-section">
        <div className="panel-header development-panel-header">
          <div>
            <h3>SQL console</h3>
            <p className="development-section-copy">
              Allowed tables: <code>email_sync_state</code>, <code>synced_emails</code>, <code>synced_email_labels</code>, <code>synced_email_attachments</code>, and <code>email_embeddings</code>.
            </p>
          </div>
          <div className="development-query-actions">
            {queryResult ? (
              <button type="button" className="secondary" onClick={downloadQueryAsCSV}>
                Download CSV
              </button>
            ) : null}
            <button type="button" onClick={runQuery} disabled={runningQuery || !sql.trim()}>
              {runningQuery ? 'Running...' : 'Run query'}
            </button>
          </div>
        </div>

        <textarea
          className="development-sql-input"
          value={sql}
          onChange={(event) => setSQL(event.target.value)}
          spellCheck="false"
        />

        <div className="development-inline-results">
          <div className="development-inline-results-header">
            <div>
              <h4>Results</h4>
            </div>
            {queryResult ? <span className="status-pill ok">{`${queryResult.row_count} rows`}</span> : null}
          </div>

          {!queryResult ? (
            <div className="info-box development-empty-state">
              <p>Run a query to inspect the email cache.</p>
            </div>
          ) : (
          <>
            {Array.isArray(queryResult.notes) && queryResult.notes.length > 0 ? (
              <div className="development-notes">
                {queryResult.notes.map((note) => (
                  <div key={note} className="note">{note}</div>
                ))}
              </div>
            ) : null}

            <div className="development-query-summary">
              <code>{queryResult.sql}</code>
            </div>

            <div className="development-table-wrapper">
              <table className="development-table">
                <thead>
                  <tr>
                    {queryResult.columns.map((column) => (
                      <th key={column}>{column}</th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {queryResult.rows.length === 0 ? (
                    <tr>
                      <td colSpan={Math.max(queryResult.columns.length, 1)} className="development-no-results">No rows returned.</td>
                    </tr>
                  ) : queryResult.rows.map((row, index) => (
                    <tr key={`row-${index}`}>
                      {queryResult.columns.map((column) => (
                        <td key={`${index}-${column}`}>{formatCellValue(row[column])}</td>
                      ))}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </>
          )}
        </div>
      </section>
    </div>
  )
}

export default Development
