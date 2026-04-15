import { useState, useRef, useEffect } from 'react'

const apiBaseUrl = import.meta.env.VITE_API_BASE_URL || `${window.location.protocol}//${window.location.hostname}:8000`
const conversationStorageKey = 'towel.chat.conversation_id'

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

function getStoredConversationId() {
  try {
    return localStorage.getItem(conversationStorageKey) || ''
  } catch {
    return ''
  }
}

function saveConversationId(value) {
  try {
    if (value) {
      localStorage.setItem(conversationStorageKey, value)
      return
    }
    localStorage.removeItem(conversationStorageKey)
  } catch {
    // ignore localStorage failures
  }
}

function newMessageId(prefix) {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return `${prefix}-${crypto.randomUUID()}`
  }
  return `${prefix}-${Date.now()}-${Math.random().toString(16).slice(2)}`
}

function Chat() {
  const [messages, setMessages] = useState([])
  const [input, setInput] = useState('')
  const [busy, setBusy] = useState(false)
  const [conversationId, setConversationId] = useState(getStoredConversationId)
  const messagesEndRef = useRef(null)
  const streamRef = useRef(null)

  const scrollToBottom = () => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }

  useEffect(() => {
    scrollToBottom()
  }, [messages])

  useEffect(() => {
    return () => {
      if (streamRef.current) {
        streamRef.current.close()
        streamRef.current = null
      }
    }
  }, [])

  useEffect(() => {
    if (!conversationId || messages.length > 0) {
      return
    }

    let active = true

    async function loadConversationHistory() {
      try {
        const response = await fetch(`${apiBaseUrl}/api/chat/conversations/${encodeURIComponent(conversationId)}`)
        if (response.status === 404) {
          if (!active) return
          setConversationId('')
          saveConversationId('')
          return
        }

        const data = await parseResponse(response)
        if (!active || !Array.isArray(data.messages)) {
          return
        }

        setMessages(
          data.messages
            .filter((item) => item.role === 'user' || item.role === 'assistant')
            .map((item) => ({
              id: `db-${item.id}`,
              role: item.role,
              content: item.content,
              actions: []
            }))
        )
      } catch (err) {
        console.error('Failed to load conversation history:', err)
      }
    }

    loadConversationHistory()

    return () => {
      active = false
    }
  }, [conversationId, messages.length])

  async function handleSubmit(e) {
    e.preventDefault()
    if (!input.trim() || busy) return

    if (streamRef.current) {
      streamRef.current.close()
      streamRef.current = null
    }

    const userMessage = input.trim()
    const userMessageId = newMessageId('user')
    const assistantMessageId = newMessageId('assistant')

    setInput('')
    setMessages((prev) => [
      ...prev,
      { id: userMessageId, role: 'user', content: userMessage },
      { id: assistantMessageId, role: 'assistant', content: '', actions: [] }
    ])
    setBusy(true)

    try {
      const sessionPayload = { message: userMessage }
      if (conversationId) {
        sessionPayload.conversation_id = conversationId
      }

      const sessionResponse = await fetch(`${apiBaseUrl}/api/chat/session`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json'
        },
        body: JSON.stringify(sessionPayload)
      })

      const session = await parseResponse(sessionResponse)
      const resolvedConversationId = session.conversation_id
      const sessionId = session.session_id || resolvedConversationId

      if (!resolvedConversationId || !sessionId) {
        throw new Error('Invalid stream session response')
      }

      setConversationId(resolvedConversationId)
      saveConversationId(resolvedConversationId)

      await new Promise((resolve, reject) => {
        const streamUrl = `${apiBaseUrl}/api/chat/stream?session_id=${encodeURIComponent(sessionId)}`
        const source = new EventSource(streamUrl)
        streamRef.current = source

        const cleanup = () => {
          if (streamRef.current === source) {
            streamRef.current = null
          }
          source.close()
        }

        source.addEventListener('token', (event) => {
          try {
            const payload = JSON.parse(event.data)
            const token = typeof payload?.v === 'string' ? payload.v : ''
            if (!token) {
              return
            }
            setMessages((prev) =>
              prev.map((msg) =>
                msg.id === assistantMessageId
                  ? {
                      ...msg,
                      content: msg.content + token
                    }
                  : msg
              )
            )
          } catch {
            // ignore malformed token chunks
          }
        })

        source.addEventListener('done', (event) => {
          try {
            const payload = JSON.parse(event.data)
            const finalResponse = typeof payload?.response === 'string' ? payload.response : ''
            const finalActions = Array.isArray(payload?.actions) ? payload.actions : []
            const finalConversationId = typeof payload?.conversation_id === 'string' ? payload.conversation_id : ''

            if (finalConversationId) {
              setConversationId(finalConversationId)
              saveConversationId(finalConversationId)
            }

            setMessages((prev) =>
              prev.map((msg) =>
                msg.id === assistantMessageId
                  ? {
                      ...msg,
                      content: finalResponse || msg.content,
                      actions: finalActions
                    }
                  : msg
              )
            )
          } catch {
            // keep tokenized content if final payload fails to parse
          }
          cleanup()
          resolve()
        })

        source.addEventListener('failed', (event) => {
          let detail = 'Stream failed'
          try {
            const payload = JSON.parse(event.data)
            if (typeof payload?.detail === 'string' && payload.detail.trim()) {
              detail = payload.detail.trim()
            }
          } catch {
            // use fallback detail
          }

          setMessages((prev) =>
            prev.map((msg) =>
              msg.id === assistantMessageId
                ? {
                    ...msg,
                    content: msg.content || `Error: ${detail}`,
                    error: true
                  }
                : msg
            )
          )

          cleanup()
          reject(new Error(detail))
        })

        source.onerror = () => {
          if (source.readyState === EventSource.CLOSED) {
            cleanup()
            reject(new Error('Stream connection closed unexpectedly'))
          }
        }
      })
    } catch (err) {
      setMessages((prev) =>
        prev.map((msg) =>
          msg.id === assistantMessageId
            ? {
                ...msg,
                content: msg.content || `Error: ${err.message}`,
                error: true
              }
            : msg
        )
      )
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="chat-container">
      <div className="chat-header">
        <p className="eyebrow">AI Assistant</p>
        <h2>Gmail Manager Chat</h2>
        <p style={{ color: 'var(--color-text-secondary)', margin: 0 }}>
          Ask me to organize your emails, create filters, or manage labels
        </p>
      </div>

      <div className="chat-messages">
        {messages.length === 0 ? (
          <div style={{ textAlign: 'center', color: 'var(--color-text-tertiary)', padding: '3rem 0' }}>
            <p style={{ fontSize: '1.125rem', marginBottom: '0.5rem' }}>👋 How can I help with your Gmail?</p>
            <p style={{ fontSize: '0.9375rem' }}>Try asking me to organize emails or create filters</p>
          </div>
        ) : (
          messages.map((msg, idx) => (
            <div key={msg.id || idx} className={`message ${msg.role}`}>
              <div className="message-avatar">
                {msg.role === 'user' ? 'You' : 'AI'}
              </div>
              <div className="message-content">
                {msg.content}
                {msg.actions && msg.actions.length > 0 ? (
                  <div className="message-actions">
                    {msg.actions.map((action, i) => (
                      <div key={i}>→ {action}</div>
                    ))}
                  </div>
                ) : null}
              </div>
            </div>
          ))
        )}
        <div ref={messagesEndRef} />
      </div>

      <div className="chat-input-container">
        <form onSubmit={handleSubmit}>
          <div className="chat-input-wrapper">
            <textarea
              className="chat-input"
              value={input}
              onChange={(e) => setInput(e.target.value)}
              placeholder="Type your message..."
              rows={1}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.shiftKey) {
                  e.preventDefault()
                  handleSubmit(e)
                }
              }}
            />
            <button type="submit" className="send-button" disabled={!input.trim() || busy}>
              {busy ? 'Sending...' : 'Send'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

export default Chat
