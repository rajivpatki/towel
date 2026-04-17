import { useEffect, useState } from 'react'
import { NavLink, useLocation, useNavigate, useMatch } from 'react-router-dom'

const apiBaseUrl = import.meta.env.VITE_API_BASE_URL || `http://localhost:8000`
const pageSize = 15
const newChatEvent = 'towel:new-chat'
const conversationRefreshEvent = 'towel:conversation-list-refresh'

async function parseResponse(response) {
  if (response.ok) {
    if (response.status === 204) {
      return null
    }
    return response.json()
  }

  // Handle unauthorized - redirect to login/setup
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

function profileFallback(name) {
  const value = (name || 'T').trim()
  return value.slice(0, 1).toUpperCase()
}

function ChatIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z" />
    </svg>
  )
}

function HistoryIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M3 3v5h5" />
      <path d="M3.05 13A9 9 0 1 0 6 5.3L3 8" />
      <path d="M12 7v5l4 2" />
    </svg>
  )
}

function SparklesIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M12 3l1.9 4.8L19 9.7l-4.8 1.9L12 16.4l-1.9-4.8L5 9.7l5.1-1.9L12 3Z" />
      <path d="M19 14l.9 2.1L22 17l-2.1.9L19 20l-.9-2.1L16 17l2.1-.9L19 14Z" />
    </svg>
  )
}

function NewChatIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M12 20h9" />
      <path d="M16.5 3.5a2.12 2.12 0 1 1 3 3L7 19l-4 1 1-4Z" />
    </svg>
  )
}

function ChevronIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.25" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="m6 9 6 6 6-6" />
    </svg>
  )
}

function SettingsIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <circle cx="12" cy="12" r="3" />
      <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1 0 2.83 2 2 0 0 1-2.83 0l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09a1.65 1.65 0 0 0-1-1.51 1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83 0 2 2 0 0 1 0-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09a1.65 1.65 0 0 0 1.51-1 1.65 1.65 0 0 0-.33-1.82L4.21 7.2a2 2 0 0 1 0-2.83 2 2 0 0 1 2.83 0l.06.06a1.65 1.65 0 0 0 1.82.33h.01A1.65 1.65 0 0 0 10 3.25V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51h.01a1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 0 2 2 0 0 1 0 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82v.01a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1Z" />
    </svg>
  )
}

function TrashIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M3 6h18" />
      <path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6" />
      <path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2" />
    </svg>
  )
}

function Sidebar({ status }) {
  const navigate = useNavigate()
  const location = useLocation()
  const chatMatch = useMatch('/chat/:conversationId')
  const currentConversationId = chatMatch?.params?.conversationId || ''
  const [isChatOpen, setIsChatOpen] = useState(true)
  const [conversations, setConversations] = useState([])
  const [page, setPage] = useState(0)
  const [hasMore, setHasMore] = useState(false)
  const [loading, setLoading] = useState(false)
  const [deletingId, setDeletingId] = useState('')

  async function loadConversations(nextPage = 1, replace = false) {
    setLoading(true)
    try {
      const response = await fetch(`${apiBaseUrl}/api/chat/conversations?page=${nextPage}&page_size=${pageSize}`, {
        credentials: 'include'
      })
      const data = await parseResponse(response)
      const items = Array.isArray(data?.items) ? data.items : []
      setConversations((previous) => replace ? items : [...previous, ...items])
      setPage(data?.page || nextPage)
      setHasMore(Boolean(data?.has_more))
    } catch (err) {
      console.error('Failed to load conversations:', err)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    if (isChatOpen) {
      loadConversations(1, true)
    }
  }, [isChatOpen])

  useEffect(() => {
    const refreshList = () => {
      if (isChatOpen) {
        loadConversations(1, true)
      }
    }

    window.addEventListener(conversationRefreshEvent, refreshList)

    return () => {
      window.removeEventListener(conversationRefreshEvent, refreshList)
    }
  }, [isChatOpen])

  function handleNewChat() {
    navigate('/chat')
    window.dispatchEvent(new Event(newChatEvent))
  }

  function handleSelectConversation(conversationId) {
    navigate(`/chat/${conversationId}`)
  }

  async function handleDeleteConversation(event, conversationId) {
    event.stopPropagation()
    if (!conversationId || deletingId) {
      return
    }
    setDeletingId(conversationId)
    try {
      const response = await fetch(`${apiBaseUrl}/api/chat/conversations/${encodeURIComponent(conversationId)}`, {
        method: 'DELETE',
        credentials: 'include'
      })
      await parseResponse(response)
      setConversations((previous) => previous.filter((item) => item.id !== conversationId))
      if (currentConversationId === conversationId) {
        handleNewChat()
      } else {
        window.dispatchEvent(new Event(conversationRefreshEvent))
      }
    } catch (err) {
      console.error('Failed to delete conversation:', err)
    } finally {
      setDeletingId('')
    }
  }

  const profileName = status?.google_name || status?.google_email || 'Towel User'
  const profilePicture = status?.google_account_connected ? `${apiBaseUrl}/api/profile/image` : ''
  const chatIsActive = location.pathname === '/chat' || location.pathname.startsWith('/chat/')

  return (
    <aside className="sidebar">
      <div className="sidebar-header">
        <div className="sidebar-profile">
          {profilePicture ? (
            <img className="sidebar-profile-photo" src={profilePicture} alt={profileName} />
          ) : (
            <div className="sidebar-profile-photo sidebar-profile-fallback">{profileFallback(profileName)}</div>
          )}
          <span className="sidebar-profile-name" title={profileName}>{profileName}</span>
        </div>
      </div>
      <button type="button" className="sidebar-new-chat-button" aria-label="New chat" onClick={handleNewChat}>
        <NewChatIcon />
      </button>
      <nav className="sidebar-nav">
        <div className={`sidebar-section${chatIsActive ? ' active' : ''}`}>
          <div className="sidebar-section-header">
            <button type="button" className={`sidebar-section-trigger${chatIsActive ? ' active' : ''}`} onClick={() => {
              setIsChatOpen((value) => !value)
              navigate('/chat')
            }}>
              <div className="nav-icon"><ChatIcon /></div>
              <span className="sidebar-trigger-label">Chats</span>
              <span className={`sidebar-chevron${isChatOpen ? ' open' : ''}`} aria-hidden="true"><ChevronIcon /></span>
            </button>
          </div>
          {isChatOpen ? (
            <div className="conversation-list">
              {conversations.map((conversation) => (
                <div key={conversation.id} className={`conversation-item${conversation.id === currentConversationId ? ' selected' : ''}`}>
                  <button type="button" className="conversation-select" onClick={() => handleSelectConversation(conversation.id)}>
                    <span className="conversation-title" title={conversation.title}>{conversation.title}</span>
                  </button>
                  <button
                    type="button"
                    className="conversation-delete"
                    aria-label={`Delete ${conversation.title}`}
                    onClick={(event) => handleDeleteConversation(event, conversation.id)}
                    disabled={deletingId === conversation.id}
                  >
                    <TrashIcon />
                  </button>
                </div>
              ))}
              {loading && conversations.length === 0 ? <div className="conversation-state">Loading chats...</div> : null}
              {!loading && conversations.length === 0 ? <div className="conversation-state">No chats yet</div> : null}
              {hasMore ? (
                <button type="button" className="conversation-load-more" onClick={() => loadConversations(page + 1)} disabled={loading}>
                  {loading ? 'Loading...' : 'Load more'}
                </button>
              ) : null}
            </div>
          ) : null}
        </div>

        <NavLink to="/history" className={({ isActive }) => isActive ? 'nav-link active' : 'nav-link'}>
          <div className="nav-icon"><HistoryIcon /></div>
          <span>History</span>
        </NavLink>

        <NavLink to="/personalise" className={({ isActive }) => isActive ? 'nav-link personalise-link active' : 'nav-link personalise-link'}>
          <div className="nav-icon"><SparklesIcon /></div>
          <span>Personalise</span>
        </NavLink>

        <NavLink to="/settings" className={({ isActive }) => isActive ? 'nav-link active' : 'nav-link'}>
          <div className="nav-icon"><SettingsIcon /></div>
          <span>Settings</span>
        </NavLink>
      </nav>
    </aside>
  )
}

export default Sidebar
