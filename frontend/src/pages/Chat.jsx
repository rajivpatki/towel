import { useState, useRef, useEffect } from 'react'

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

function Chat() {
  const [messages, setMessages] = useState([])
  const [input, setInput] = useState('')
  const [busy, setBusy] = useState(false)
  const messagesEndRef = useRef(null)

  const scrollToBottom = () => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }

  useEffect(() => {
    scrollToBottom()
  }, [messages])

  async function handleSubmit(e) {
    e.preventDefault()
    if (!input.trim() || busy) return

    const userMessage = input.trim()
    setInput('')
    setMessages((prev) => [...prev, { role: 'user', content: userMessage }])
    setBusy(true)

    try {
      const response = await fetch(`${apiBaseUrl}/api/chat`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json'
        },
        body: JSON.stringify({ message: userMessage })
      })
      const data = await parseResponse(response)
      
      setMessages((prev) => [
        ...prev,
        {
          role: 'assistant',
          content: data.response,
          actions: data.actions || []
        }
      ])
    } catch (err) {
      setMessages((prev) => [
        ...prev,
        {
          role: 'assistant',
          content: `Error: ${err.message}`,
          error: true
        }
      ])
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
            <div key={idx} className={`message ${msg.role}`}>
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
