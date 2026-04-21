import { useEffect, useRef, useState } from 'react'
import { useToast } from '../components/ToastProvider'

const apiBaseUrl = import.meta.env.VITE_API_BASE_URL || `http://localhost:8000`
const scheduledTaskPageSize = 50
const searchDebounceMs = 250

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

function PlusIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M12 5v14" />
      <path d="M5 12h14" />
    </svg>
  )
}

function PencilIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M12 20h9" />
      <path d="M16.5 3.5a2.12 2.12 0 1 1 3 3L7 19l-4 1 1-4Z" />
    </svg>
  )
}

function TrashIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M3 6h18" />
      <path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6" />
      <path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2" />
    </svg>
  )
}

function CheckIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.25" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="m5 12 5 5L20 7" />
    </svg>
  )
}

function XIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.25" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M18 6 6 18" />
      <path d="m6 6 12 12" />
    </svg>
  )
}

function formatTimestamp(value) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return ''
  }
  const formatter = new Intl.DateTimeFormat('en-GB', {
    day: '2-digit',
    month: 'short',
    year: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false
  })
  const parts = formatter.formatToParts(date)
  const map = Object.fromEntries(parts.map((part) => [part.type, part.value]))
  return `${map.day} ${map.month} ${map.year} ${map.hour}:${map.minute}`
}

function buildScheduledTasksUrl(query, page) {
  const url = new URL('/api/scheduled-tasks', apiBaseUrl)
  if (query) {
    url.searchParams.set('q', query)
    return url.toString()
  }
  url.searchParams.set('page', String(page))
  url.searchParams.set('page_size', String(scheduledTaskPageSize))
  return url.toString()
}

function parseLabelNames(value) {
  return value
    .split(',')
    .map((label) => label.trim())
    .filter(Boolean)
}

function labelNamesToText(labelNames) {
  return Array.isArray(labelNames) ? labelNames.join(', ') : ''
}

function createEmptyTaskForm() {
  return {
    title: '',
    instruction: '',
    labelNamesText: '',
    requireInInbox: false
  }
}

function runStatusTone(status) {
  switch ((status || '').toLowerCase()) {
    case 'success':
      return 'ok'
    case 'running':
      return 'pending'
    case 'failed':
      return 'failed'
    case 'skipped':
      return 'neutral'
    default:
      return 'neutral'
  }
}

function runStatusLabel(status) {
  switch ((status || '').toLowerCase()) {
    case 'success':
      return 'Success'
    case 'running':
      return 'Running'
    case 'failed':
      return 'Failed'
    case 'skipped':
      return 'Skipped'
    default:
      return 'Idle'
  }
}

function ScheduledTasks() {
  const { showToast } = useToast()
  const [tasks, setTasks] = useState([])
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [savingId, setSavingId] = useState(0)
  const [editingId, setEditingId] = useState(0)
  const [newTask, setNewTask] = useState(createEmptyTaskForm())
  const [editTask, setEditTask] = useState(createEmptyTaskForm())
  const [searchInput, setSearchInput] = useState('')
  const [searchQuery, setSearchQuery] = useState('')
  const [selectedIds, setSelectedIds] = useState([])
  const [currentPage, setCurrentPage] = useState(1)
  const [hasMore, setHasMore] = useState(false)
  const loadMoreRef = useRef(null)
  const requestIdRef = useRef(0)
  const loadingMoreRef = useRef(false)

  useEffect(() => {
    loadingMoreRef.current = loadingMore
  }, [loadingMore])

  async function loadTasks({ query, page, append }) {
    const requestId = requestIdRef.current + 1
    requestIdRef.current = requestId

    if (append) {
      setLoadingMore(true)
    } else {
      setLoading(true)
    }

    try {
      const response = await fetch(buildScheduledTasksUrl(query, page), {
        credentials: 'include'
      })
      const data = await parseResponse(response)
      if (requestId !== requestIdRef.current) {
        return
      }

      const nextTasks = Array.isArray(data.tasks) ? data.tasks : []
      setTasks((previous) => {
        if (!append) {
          return nextTasks
        }
        const existing = new Set(previous.map((task) => task.id))
        return [...previous, ...nextTasks.filter((task) => !existing.has(task.id))]
      })
      setCurrentPage(Number.isFinite(data.page) && data.page > 0 ? data.page : page)
      setHasMore(Boolean(data.has_more) && !query)
    } catch (err) {
      if (requestId === requestIdRef.current) {
        showToast({
          tone: 'error',
          title: 'Unable to load scheduled tasks',
          description: err.message
        })
      }
    } finally {
      if (requestId === requestIdRef.current) {
        setLoading(false)
        setLoadingMore(false)
      }
    }
  }

  function cancelEdit() {
    setEditingId(0)
    setEditTask(createEmptyTaskForm())
  }

  async function refreshCurrentView() {
    setSelectedIds([])
    cancelEdit()
    await loadTasks({ query: searchQuery, page: 1, append: false })
  }

  useEffect(() => {
    const timeoutId = window.setTimeout(() => {
      setSearchQuery(searchInput.trim())
    }, searchDebounceMs)
    return () => window.clearTimeout(timeoutId)
  }, [searchInput])

  useEffect(() => {
    setSelectedIds([])
    cancelEdit()
    loadTasks({ query: searchQuery, page: 1, append: false })
  }, [searchQuery])

  useEffect(() => {
    if (searchQuery || !hasMore || loading || loadingMore) {
      return
    }
    const node = loadMoreRef.current
    if (!node) {
      return
    }

    const observer = new IntersectionObserver((entries) => {
      const entry = entries[0]
      if (!entry?.isIntersecting || loadingMoreRef.current) {
        return
      }
      loadTasks({ query: '', page: currentPage + 1, append: true })
    }, { rootMargin: '220px 0px 220px 0px' })

    observer.observe(node)
    return () => observer.disconnect()
  }, [currentPage, hasMore, loading, loadingMore, searchQuery])

  function beginEdit(task) {
    setEditingId(task.id)
    setEditTask({
      title: task.title || '',
      instruction: task.instruction || '',
      labelNamesText: labelNamesToText(task.label_names),
      requireInInbox: Boolean(task.require_in_inbox)
    })
  }

  function toggleSelected(id) {
    setSelectedIds((previous) => (
      previous.includes(id) ? previous.filter((value) => value !== id) : [...previous, id]
    ))
  }

  async function createTask() {
    const title = newTask.title.trim()
    const instruction = newTask.instruction.trim()
    if (!title || !instruction) {
      return
    }

    setCreating(true)
    try {
      const response = await fetch(`${apiBaseUrl}/api/scheduled-tasks`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json'
        },
        credentials: 'include',
        body: JSON.stringify({
          title,
          instruction,
          enabled: true,
          require_in_inbox: newTask.requireInInbox,
          label_names: parseLabelNames(newTask.labelNamesText)
        })
      })
      await parseResponse(response)
      setNewTask(createEmptyTaskForm())
      await refreshCurrentView()
    } catch (err) {
      showToast({
        tone: 'error',
        title: 'Unable to create scheduled task',
        description: err.message
      })
    } finally {
      setCreating(false)
    }
  }

  async function saveEdit(id) {
    const title = editTask.title.trim()
    const instruction = editTask.instruction.trim()
    if (!title || !instruction) {
      return
    }

    setSavingId(id)
    try {
      const response = await fetch(`${apiBaseUrl}/api/scheduled-tasks/${encodeURIComponent(id)}`, {
        method: 'PUT',
        headers: {
          'Content-Type': 'application/json'
        },
        credentials: 'include',
        body: JSON.stringify({
          title,
          instruction,
          enabled: true,
          require_in_inbox: editTask.requireInInbox,
          label_names: parseLabelNames(editTask.labelNamesText)
        })
      })
      await parseResponse(response)
      await refreshCurrentView()
    } catch (err) {
      showToast({
        tone: 'error',
        title: 'Unable to update scheduled task',
        description: err.message
      })
    } finally {
      setSavingId(0)
    }
  }

  async function deleteSelected() {
    if (selectedIds.length === 0) {
      return
    }

    setDeleting(true)
    try {
      const response = await fetch(`${apiBaseUrl}/api/scheduled-tasks`, {
        method: 'DELETE',
        headers: {
          'Content-Type': 'application/json'
        },
        credentials: 'include',
        body: JSON.stringify({ ids: selectedIds })
      })
      await parseResponse(response)
      await refreshCurrentView()
    } catch (err) {
      showToast({
        tone: 'error',
        title: 'Unable to delete scheduled tasks',
        description: err.message
      })
    } finally {
      setDeleting(false)
    }
  }

  async function deleteOne(id) {
    setDeleting(true)
    try {
      const response = await fetch(`${apiBaseUrl}/api/scheduled-tasks`, {
        method: 'DELETE',
        headers: {
          'Content-Type': 'application/json'
        },
        credentials: 'include',
        body: JSON.stringify({ ids: [id] })
      })
      await parseResponse(response)
      await refreshCurrentView()
    } catch (err) {
      showToast({
        tone: 'error',
        title: 'Unable to delete scheduled task',
        description: err.message
      })
    } finally {
      setDeleting(false)
    }
  }

  const emptyStateCopy = loading
    ? 'Loading...'
    : searchQuery
      ? 'No matching scheduled tasks.'
      : 'No scheduled tasks yet.'

  return (
    <div className="preferences-container memories-page scheduled-tasks-page">
      <div className="memories-header scheduled-tasks-header">
        <div className="memories-title-row">
          {selectedIds.length > 0 ? (
            <button
              type="button"
              className="memory-icon-button memory-bulk-delete"
              onClick={deleteSelected}
              disabled={deleting}
              aria-label={`Delete ${selectedIds.length} selected scheduled tasks`}
              title={`Delete ${selectedIds.length} selected scheduled tasks`}
            >
              <TrashIcon />
            </button>
          ) : null}
        </div>
        <div className="note scheduled-tasks-note">
          Scheduled tasks run automatically only on emails updated during history-based sync ticks, then optionally narrow by Inbox and label filters.
        </div>
      </div>

      <input
        className="memory-search-input"
        value={searchInput}
        onChange={(e) => setSearchInput(e.target.value)}
        placeholder="Search scheduled tasks"
        type="search"
        spellCheck="false"
      />

      <div className="preference-item scheduled-task-compose-card">
        <div className="scheduled-task-compose-grid">
          <div className="scheduled-task-title-row">
            <input
              className="scheduled-task-title-input"
              value={newTask.title}
              onChange={(e) => setNewTask((previous) => ({ ...previous, title: e.target.value }))}
              placeholder="Task title"
            />
            <label className="scheduled-task-toggle-wrapper">
              <input
                type="checkbox"
                className="scheduled-task-toggle-input"
                checked={newTask.requireInInbox}
                onChange={(e) => setNewTask((previous) => ({ ...previous, requireInInbox: e.target.checked }))}
              />
              <span className="scheduled-task-toggle-track">
                <span className="scheduled-task-toggle-thumb" />
              </span>
              <span className="scheduled-task-toggle-label">Inbox only</span>
            </label>
          </div>
          <textarea
            className="memory-edit-input scheduled-task-compose-textarea"
            value={newTask.instruction}
            onChange={(e) => setNewTask((previous) => ({ ...previous, instruction: e.target.value }))}
            placeholder="Instruction for the autonomous task"
          />
          <div className="scheduled-task-label-row">
            <input
              className="scheduled-task-label-input"
              value={newTask.labelNamesText}
              onChange={(e) => setNewTask((previous) => ({ ...previous, labelNamesText: e.target.value }))}
              placeholder="Labels filter, comma separated"
            />
            <button
              type="button"
              className="memory-compose-submit scheduled-task-compose-submit"
              onClick={createTask}
              disabled={creating}
              aria-label="Add scheduled task"
              title="Add scheduled task"
            >
              {creating ? '...' : <PlusIcon />}
            </button>
          </div>
        </div>
      </div>

      <div className="memory-list">
        {tasks.length === 0 ? (
          <div className="memory-empty-state">
            <p>{emptyStateCopy}</p>
          </div>
        ) : (
          tasks.map((task) => {
            const labelNames = Array.isArray(task.label_names) ? task.label_names : []
            const runMessage = task.last_run_error || task.last_run_message || ''
            const filters = []
            if (task.require_in_inbox) {
              filters.push('Inbox only')
            }
            if (labelNames.length > 0) {
              filters.push(...labelNames)
            }
            if (filters.length === 0) {
              filters.push('All updated emails')
            }

            return (
              <div key={task.id} className={`preference-item memory-card scheduled-task-card${selectedIds.includes(task.id) ? ' selected' : ''}`}>
                <label className="memory-select" aria-label="Select scheduled task">
                  <input
                    type="checkbox"
                    checked={selectedIds.includes(task.id)}
                    onChange={() => toggleSelected(task.id)}
                  />
                </label>
                <div className="memory-card-body">
                  {editingId === task.id ? (
                    <div className="scheduled-task-edit-grid">
                      <div className="scheduled-task-title-row">
                        <input
                          className="scheduled-task-title-input"
                          value={editTask.title}
                          onChange={(e) => setEditTask((previous) => ({ ...previous, title: e.target.value }))}
                          placeholder="Task title"
                        />
                        <label className="scheduled-task-toggle-wrapper">
                          <input
                            type="checkbox"
                            className="scheduled-task-toggle-input"
                            checked={editTask.requireInInbox}
                            onChange={(e) => setEditTask((previous) => ({ ...previous, requireInInbox: e.target.checked }))}
                          />
                          <span className="scheduled-task-toggle-track">
                            <span className="scheduled-task-toggle-thumb" />
                          </span>
                          <span className="scheduled-task-toggle-label">Inbox only</span>
                        </label>
                      </div>
                      <textarea
                        className="memory-edit-input scheduled-task-compose-textarea"
                        value={editTask.instruction}
                        onChange={(e) => setEditTask((previous) => ({ ...previous, instruction: e.target.value }))}
                      />
                      <input
                        className="scheduled-task-label-input"
                        value={editTask.labelNamesText}
                        onChange={(e) => setEditTask((previous) => ({ ...previous, labelNamesText: e.target.value }))}
                        placeholder="Labels filter, comma separated"
                      />
                    </div>
                  ) : (
                    <>
                      <div className="scheduled-task-card-title-row">
                        <div className="scheduled-task-card-title-wrap">
                          <div className="scheduled-task-card-title">{task.title}</div>
                          <div className="scheduled-task-pill-row">
                            {filters.map((filter) => (
                              <span key={`${task.id}-${filter}`} className="scheduled-task-pill">{filter}</span>
                            ))}
                          </div>
                        </div>
                      </div>
                      <div className="memory-card-text scheduled-task-card-text">{task.instruction}</div>
                      {runMessage ? (
                        <div className="scheduled-task-run-summary">
                          <span className={`status-pill ${runStatusTone(task.last_run_status)}`}>{runStatusLabel(task.last_run_status)}</span>
                          <div className="scheduled-task-run-text">{runMessage}</div>
                        </div>
                      ) : null}
                    </>
                  )}
                  <div className="memory-card-meta">
                    Updated {formatTimestamp(task.updated_at)}
                  </div>
                </div>
                <div className="memory-card-actions">
                  {editingId === task.id ? (
                    <>
                      <button
                        type="button"
                        className="memory-icon-button"
                        onClick={() => saveEdit(task.id)}
                        disabled={savingId === task.id}
                        aria-label="Save scheduled task"
                        title="Save scheduled task"
                      >
                        <CheckIcon />
                      </button>
                      <button
                        type="button"
                        className="memory-icon-button"
                        onClick={cancelEdit}
                        aria-label="Cancel editing"
                        title="Cancel editing"
                      >
                        <XIcon />
                      </button>
                    </>
                  ) : (
                    <>
                      <button
                        type="button"
                        className="memory-icon-button"
                        onClick={() => beginEdit(task)}
                        aria-label="Edit scheduled task"
                        title="Edit scheduled task"
                      >
                        <PencilIcon />
                      </button>
                      <button
                        type="button"
                        className="memory-icon-button memory-delete-button"
                        onClick={() => deleteOne(task.id)}
                        disabled={deleting}
                        aria-label="Delete scheduled task"
                        title="Delete scheduled task"
                      >
                        <TrashIcon />
                      </button>
                    </>
                  )}
                </div>
              </div>
            )
          })
        )}
      </div>

      {!searchQuery ? (
        <div ref={loadMoreRef} className="memory-load-more-anchor" aria-hidden="true">
          {loadingMore ? <span className="memory-load-more-text">Loading more...</span> : null}
        </div>
      ) : null}
    </div>
  )
}

export default ScheduledTasks
