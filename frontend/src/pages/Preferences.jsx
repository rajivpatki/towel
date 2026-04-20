import { useEffect, useRef, useState } from 'react'
import { useToast } from '../components/ToastProvider'

const apiBaseUrl = import.meta.env.VITE_API_BASE_URL || `http://localhost:8000`
const memoryPageSize = 50
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

function formatMemoryTimestamp(value) {
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

function buildMemoriesUrl(query, page) {
  const url = new URL('/api/memories', apiBaseUrl)
  if (query) {
    url.searchParams.set('q', query)
    return url.toString()
  }
  url.searchParams.set('page', String(page))
  url.searchParams.set('page_size', String(memoryPageSize))
  return url.toString()
}

function Preferences() {
  const { showToast } = useToast()
  const [memories, setMemories] = useState([])
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [savingId, setSavingId] = useState(0)
  const [editingId, setEditingId] = useState(0)
  const [editingText, setEditingText] = useState('')
  const [newMemoryText, setNewMemoryText] = useState('')
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

  async function loadMemories({ query, page, append }) {
    const requestId = requestIdRef.current + 1
    requestIdRef.current = requestId

    if (append) {
      setLoadingMore(true)
    } else {
      setLoading(true)
    }

    try {
      const response = await fetch(buildMemoriesUrl(query, page), {
        credentials: 'include'
      })
      const data = await parseResponse(response)
      if (requestId !== requestIdRef.current) {
        return
      }

      const nextMemories = Array.isArray(data.memories) ? data.memories : []
      setMemories((previous) => {
        if (!append) {
          return nextMemories
        }
        const existing = new Set(previous.map((memory) => memory.id))
        return [...previous, ...nextMemories.filter((memory) => !existing.has(memory.id))]
      })
      setCurrentPage(Number.isFinite(data.page) && data.page > 0 ? data.page : page)
      setHasMore(Boolean(data.has_more) && !query)
    } catch (err) {
      if (requestId === requestIdRef.current) {
        showToast({
          tone: 'error',
          title: 'Unable to load memories',
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

  async function refreshCurrentView() {
    setSelectedIds([])
    cancelEdit()
    await loadMemories({ query: searchQuery, page: 1, append: false })
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
    loadMemories({ query: searchQuery, page: 1, append: false })
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
      loadMemories({ query: '', page: currentPage + 1, append: true })
    }, { rootMargin: '220px 0px 220px 0px' })

    observer.observe(node)
    return () => observer.disconnect()
  }, [currentPage, hasMore, loading, loadingMore, searchQuery])

  function beginEdit(memory) {
    setEditingId(memory.id)
    setEditingText(memory.text)
  }

  function cancelEdit() {
    setEditingId(0)
    setEditingText('')
  }

  async function createMemory() {
    const text = newMemoryText.trim()
    if (!text) {
      return
    }

    setCreating(true)
    try {
      const response = await fetch(`${apiBaseUrl}/api/memories`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json'
        },
        credentials: 'include',
        body: JSON.stringify({ text })
      })
      await parseResponse(response)
      setNewMemoryText('')
      await refreshCurrentView()
    } catch (err) {
      showToast({
        tone: 'error',
        title: 'Unable to save memory',
        description: err.message
      })
    } finally {
      setCreating(false)
    }
  }

  async function saveEdit(id) {
    const text = editingText.trim()
    if (!text) {
      return
    }

    setSavingId(id)
    try {
      const response = await fetch(`${apiBaseUrl}/api/memories/${encodeURIComponent(id)}`, {
        method: 'PUT',
        headers: {
          'Content-Type': 'application/json'
        },
        credentials: 'include',
        body: JSON.stringify({ text })
      })
      await parseResponse(response)
      await refreshCurrentView()
    } catch (err) {
      showToast({
        tone: 'error',
        title: 'Unable to update memory',
        description: err.message
      })
    } finally {
      setSavingId(0)
    }
  }

  function toggleSelected(id) {
    setSelectedIds((previous) => (
      previous.includes(id) ? previous.filter((value) => value !== id) : [...previous, id]
    ))
  }

  async function deleteSelected() {
    if (selectedIds.length === 0) {
      return
    }

    setDeleting(true)
    try {
      const response = await fetch(`${apiBaseUrl}/api/memories`, {
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
        title: 'Unable to delete memories',
        description: err.message
      })
    } finally {
      setDeleting(false)
    }
  }

  async function deleteOne(id) {
    setDeleting(true)
    try {
      const response = await fetch(`${apiBaseUrl}/api/memories`, {
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
        title: 'Unable to delete memory',
        description: err.message
      })
    } finally {
      setDeleting(false)
    }
  }

  const emptyStateCopy = loading
    ? 'Loading...'
    : searchQuery
      ? 'No matching memories.'
      : 'No memories yet.'

  return (
    <div className="preferences-container memories-page">
      <div className="memories-header">
        {/* <p className="eyebrow">Personalise</p> */}
        <div className="memories-title-row">
          {/* <h2>Memories</h2> */}
          {selectedIds.length > 0 ? (
            <button
              type="button"
              className="memory-icon-button memory-bulk-delete"
              onClick={deleteSelected}
              disabled={deleting}
              aria-label={`Delete ${selectedIds.length} selected memories`}
              title={`Delete ${selectedIds.length} selected memories`}
            >
              <TrashIcon />
            </button>
          ) : null}
        </div>
      </div>

      <input
        className="memory-search-input"
        value={searchInput}
        onChange={(e) => setSearchInput(e.target.value)}
        placeholder="Search memories"
        type="search"
        spellCheck="false"
      />

      <div className="memory-compose-card">
        <input
          id="new-memory-text"
          className="memory-compose-input"
          value={newMemoryText}
          onChange={(e) => setNewMemoryText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              createMemory()
            }
          }}
          placeholder="Add memory"
        />
        <button
          type="button"
          className="memory-compose-submit"
          onClick={createMemory}
          disabled={creating}
          aria-label="Add memory"
          title="Add memory"
        >
          {creating ? '...' : <PlusIcon />}
        </button>
      </div>

      <div className="memory-list">
        {memories.length === 0 ? (
          <div className="memory-empty-state">
            <p>{emptyStateCopy}</p>
          </div>
        ) : (
          memories.map((memory) => (
            <div key={memory.id} className={`preference-item memory-card${selectedIds.includes(memory.id) ? ' selected' : ''}`}>
              <label className="memory-select" aria-label="Select memory">
                <input
                  type="checkbox"
                  checked={selectedIds.includes(memory.id)}
                  onChange={() => toggleSelected(memory.id)}
                />
              </label>
              <div className="memory-card-body">
                {editingId === memory.id ? (
                  <textarea
                    className="memory-edit-input"
                    value={editingText}
                    onChange={(e) => setEditingText(e.target.value)}
                  />
                ) : (
                  <div className="memory-card-text">{memory.text}</div>
                )}
                <div className="memory-card-meta">
                  Updated {formatMemoryTimestamp(memory.updated_at)}
                </div>
              </div>
              <div className="memory-card-actions">
                {editingId === memory.id ? (
                  <>
                    <button
                      type="button"
                      className="memory-icon-button"
                      onClick={() => saveEdit(memory.id)}
                      disabled={savingId === memory.id}
                      aria-label="Save memory"
                      title="Save memory"
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
                      onClick={() => beginEdit(memory)}
                      aria-label="Edit memory"
                      title="Edit memory"
                    >
                      <PencilIcon />
                    </button>
                    <button
                      type="button"
                      className="memory-icon-button memory-delete-button"
                      onClick={() => deleteOne(memory.id)}
                      disabled={deleting}
                      aria-label="Delete memory"
                      title="Delete memory"
                    >
                      <TrashIcon />
                    </button>
                  </>
                )}
              </div>
            </div>
          ))
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

export default Preferences
