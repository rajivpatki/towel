import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import ReactMarkdown from 'react-markdown'
import rehypeRaw from 'rehype-raw'
import remarkGfm from 'remark-gfm'
import streamManager from '../services/streamManager'

const apiBaseUrl = import.meta.env.VITE_API_BASE_URL || `http://127.0.0.1:8000`
const newChatEvent = 'towel:new-chat'
const conversationRefreshEvent = 'towel:conversation-list-refresh'

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

function newMessageId(prefix) {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return `${prefix}-${crypto.randomUUID()}`
  }
  return `${prefix}-${Date.now()}-${Math.random().toString(16).slice(2)}`
}

function notifyNewChat() {
  window.dispatchEvent(new Event(newChatEvent))
  window.dispatchEvent(new Event(conversationRefreshEvent))
}

function MarkdownMessage({ content }) {
  return (
    <ReactMarkdown rehypePlugins={[rehypeRaw]} remarkPlugins={[remarkGfm]}>
      {content || ''}
    </ReactMarkdown>
  )
}

function Chat({ urlConversationId = '', status }) {
  const navigate = useNavigate()
  const [messages, setMessages] = useState([])
  const [input, setInput] = useState('')
  const [busy, setBusy] = useState(false)
  const [conversationId, setConversationId] = useState(urlConversationId)
  const messagesEndRef = useRef(null)
  const streamUnsubscribeRef = useRef(null)
  const textareaRef = useRef(null)

  const profilePicture = status?.google_account_connected ? `${apiBaseUrl}/api/profile/image` : ''

  // Auto-resize textarea based on content
  useEffect(() => {
    const textarea = textareaRef.current
    if (!textarea) return
    textarea.style.height = 'auto'
    textarea.style.height = `${Math.min(textarea.scrollHeight, window.innerHeight * 0.2)}px`
  }, [input])

  // Sync conversationId with URL
  useEffect(() => {
    setConversationId(urlConversationId)
  }, [urlConversationId])

  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

  // Handle new chat event from sidebar
  useEffect(() => {
    const handleNewChat = () => {
      setBusy(false)
      setInput('')
      setMessages([])
    }

    window.addEventListener(newChatEvent, handleNewChat)

    return () => {
      window.removeEventListener(newChatEvent, handleNewChat)
    }
  }, [])

  // Load conversation history and check for active streams

  useEffect(() => {
    if (!conversationId) {
      setMessages([])
      return
    }

    let active = true
    let isStreamActive = false
    let hasLocalActiveStream = false
    let preloadedStreamState = null
    let shouldReconnect = false
    let pendingUserMessage = ''
    let streamProgressContent = ''
    let streamProgressActions = []

    async function loadConversation() {
      try {
        // Step 1: Prefer the in-memory active stream state to avoid redundant backend checks.
        const streamState = streamManager.getStreamState(conversationId)
        if (streamState && streamState.isActive) {
          isStreamActive = true
          hasLocalActiveStream = true
          pendingUserMessage = streamState.pendingUserMessage || ''
          streamProgressContent = streamState.currentContent || ''
          streamProgressActions = streamState.currentActions || []
        } else {
          // Only hit the backend if we do not already have a live connection locally.
          const backendState = await streamManager.fetchStreamState(conversationId)
          preloadedStreamState = backendState
          if (backendState && !backendState.completed && !backendState.has_error) {
            isStreamActive = true
            shouldReconnect = true
            streamProgressContent = backendState.content || ''
            streamProgressActions = backendState.actions || []
          }
        }

        // Step 2: Load conversation history from DB
        const response = await fetch(`${apiBaseUrl}/api/chat/conversations/${encodeURIComponent(conversationId)}`)
        if (response.status === 404) {
          if (isStreamActive) {
            if (!active) return
            setBusy(true)
            setMessages((prev) => {
              if (prev.length > 0) {
                return prev
              }

              const nextMessages = []
              if (pendingUserMessage) {
                nextMessages.push({
                  id: newMessageId('user'),
                  role: 'user',
                  content: pendingUserMessage,
                  actions: [],
                  streaming: false
                })
              }
              nextMessages.push({
                id: newMessageId('assistant'),
                role: 'assistant',
                content: streamProgressContent,
                actions: streamProgressActions,
                streaming: true
              })
              return nextMessages
            })

            if (hasLocalActiveStream) {
              const unsubscribe = subscribeToStream(conversationId)
              if (unsubscribe) {
                streamUnsubscribeRef.current = unsubscribe
              }
            } else if (shouldReconnect) {
              const unsubscribe = await streamManager.reconnectToStream(conversationId, {
                onProgress: (progress) => {
                  setMessages((prev) =>
                    prev.map((msg) =>
                      msg.streaming
                        ? {
                            ...msg,
                            content: progress.content || msg.content,
                            actions: progress.actions || msg.actions
                          }
                        : msg
                    )
                  )
                },
                onContent: (content) => {
                  setMessages((prev) =>
                    prev.map((msg) =>
                      msg.streaming ? { ...msg, content } : msg
                    )
                  )
                },
                onComplete: (completedResponse) => {
                  setBusy(false)
                  setMessages((prev) =>
                    prev.map((msg) =>
                      msg.streaming
                        ? {
                            id: `stream-${Date.now()}`,
                            role: 'assistant',
                            content: completedResponse.content,
                            actions: completedResponse.actions || [],
                            streaming: false
                          }
                        : msg
                    )
                  )
                  window.dispatchEvent(new Event(conversationRefreshEvent))
                },
                onError: (error) => {
                  setBusy(false)
                  setMessages((prev) =>
                    prev.map((msg) =>
                      msg.streaming
                        ? { ...msg, content: msg.content || `Error: ${error}`, error: true, streaming: false }
                        : msg
                    )
                  )
                },
                onStopped: () => {
                  setBusy(false)
                  setMessages((prev) =>
                    prev.map((msg) =>
                      msg.streaming
                        ? { ...msg, streaming: false }
                        : msg
                    )
                  )
                },
                onInactive: () => {
                  setBusy(false)
                }
              }, preloadedStreamState)

              if (unsubscribe) {
                streamUnsubscribeRef.current = unsubscribe
              }
            }
            return
          }

          if (!active) return
          navigate('/chat', { replace: true })
          notifyNewChat()
          return
        }

        const data = await parseResponse(response)
        if (!active || !Array.isArray(data.messages)) return

        // Step 3: Process DB messages
        let dbMessages = data.messages
          .filter((item) => item.role === 'user' || item.role === 'assistant')
          .map((item) => ({
            id: `db-${item.id}`,
            role: item.role,
            content: item.content,
            actions: [],
            streaming: false
          }))

        if (isStreamActive && dbMessages.length === 0 && pendingUserMessage) {
          dbMessages = [
            {
              id: newMessageId('user'),
              role: 'user',
              content: pendingUserMessage,
              actions: [],
              streaming: false
            }
          ]
        }

        // Step 4: Set messages with streaming message if active
        if (isStreamActive) {
          setBusy(true)
          setMessages([
            ...dbMessages,
            {
              id: newMessageId('assistant'),
              role: 'assistant',
              content: streamProgressContent,
              actions: streamProgressActions,
              streaming: true
            }
          ])
        } else {
          setMessages(dbMessages)
        }

        // Step 5: Subscribe to stream updates if active
        if (isStreamActive) {
          if (hasLocalActiveStream) {
            const unsubscribe = subscribeToStream(conversationId)
            if (unsubscribe) {
              streamUnsubscribeRef.current = unsubscribe
            }
          } else if (shouldReconnect) {
            const unsubscribe = await streamManager.reconnectToStream(conversationId, {
              onProgress: (progress) => {
                setMessages((prev) =>
                  prev.map((msg) =>
                    msg.streaming
                      ? {
                          ...msg,
                          content: progress.content || msg.content,
                          actions: progress.actions || msg.actions
                        }
                      : msg
                  )
                )
              },
              onContent: (content) => {
                setMessages((prev) =>
                  prev.map((msg) =>
                    msg.streaming ? { ...msg, content } : msg
                  )
                )
              },
              onComplete: (response) => {
                setBusy(false)
                setMessages((prev) =>
                  prev.map((msg) =>
                    msg.streaming
                      ? {
                          id: `stream-${Date.now()}`,
                          role: 'assistant',
                          content: response.content,
                          actions: response.actions || [],
                          streaming: false
                        }
                      : msg
                  )
                )
                window.dispatchEvent(new Event(conversationRefreshEvent))
              },
              onError: (error) => {
                setBusy(false)
                setMessages((prev) =>
                  prev.map((msg) =>
                    msg.streaming
                      ? { ...msg, content: msg.content || `Error: ${error}`, error: true, streaming: false }
                      : msg
                  )
                )
              },
              onStopped: () => {
                setBusy(false)
                setMessages((prev) =>
                  prev.map((msg) =>
                    msg.streaming
                      ? { ...msg, streaming: false }
                      : msg
                  )
                )
              },
              onInactive: () => {
                setBusy(false)
              }
            }, preloadedStreamState)

            if (unsubscribe) {
              streamUnsubscribeRef.current = unsubscribe
            }
          }
        }
      } catch (err) {
        console.error('Failed to load conversation:', err)
        setBusy(false)
      }
    }

    loadConversation()

    return () => {
      active = false
      if (streamUnsubscribeRef.current) {
        streamUnsubscribeRef.current()
        streamUnsubscribeRef.current = null
      }
    }
  }, [conversationId, navigate])

  function subscribeToStream(convId) {
    if (streamUnsubscribeRef.current) {
      streamUnsubscribeRef.current()
    }

    streamUnsubscribeRef.current = streamManager.subscribe(convId, {
      onProgress: (progress) => {
        setMessages((prev) =>
          prev.map((msg) =>
            msg.streaming
              ? {
                  ...msg,
                  content: progress.content || msg.content,
                  actions: progress.actions || msg.actions
                }
              : msg
          )
        )
      },
      onContent: (content) => {
        setMessages((prev) =>
          prev.map((msg) =>
            msg.streaming ? { ...msg, content } : msg
          )
        )
      },
      onComplete: (response) => {
        setBusy(false)
        setMessages((prev) =>
          prev.map((msg) =>
            msg.streaming
              ? {
                  id: `stream-${Date.now()}`,
                  role: 'assistant',
                  content: response.content,
                  actions: response.actions || [],
                  streaming: false
                }
              : msg
          )
        )
        // Update URL if conversation ID changed
        if (response.conversation_id && response.conversation_id !== convId) {
          navigate(`/chat/${response.conversation_id}`, { replace: true })
        }
        window.dispatchEvent(new Event(conversationRefreshEvent))
      },
      onError: (error) => {
        setBusy(false)
        setMessages((prev) =>
          prev.map((msg) =>
            msg.streaming
              ? { ...msg, content: msg.content || `Error: ${error}`, error: true, streaming: false }
              : msg
          )
        )
      },
      onStopped: () => {
        setBusy(false)
        setMessages((prev) =>
          prev.map((msg) =>
            msg.streaming
              ? { ...msg, streaming: false }
              : msg
          )
        )
      },
      onInactive: () => {
        setBusy(false)
      }
    })
  }

  async function handleStop() {
    if (!busy || !conversationId) {
      return
    }

    try {
      await streamManager.stopStream(conversationId)
    } catch (err) {
      setBusy(false)
      setMessages((prev) =>
        prev.map((msg) =>
          msg.streaming
            ? { ...msg, content: msg.content || `Error: ${err.message}`, error: true, streaming: false }
            : msg
        )
      )
    }
  }

  async function handleSubmit(event) {
    event.preventDefault()
    if (!input.trim() || busy) {
      return
    }

    const userMessage = input.trim()
    const userMessageId = newMessageId('user')
    const assistantMessageId = newMessageId('assistant')

    setInput('')
    setMessages((prev) => [
      ...prev,
      { id: userMessageId, role: 'user', content: userMessage },
      { id: assistantMessageId, role: 'assistant', content: '', actions: [], streaming: true }
    ])
    setBusy(true)

    try {
      // Use streamManager to handle the SSE connection
      await streamManager.startStream(
        conversationId,
        userMessage,
        {
          onProgress: (progress) => {
            setMessages((prev) =>
              prev.map((msg) =>
                msg.id === assistantMessageId || msg.streaming
                  ? {
                      ...msg,
                      content: progress.content || msg.content,
                      actions: progress.actions || msg.actions
                    }
                  : msg
              )
            )
          },
          onContent: (content) => {
            setMessages((prev) =>
              prev.map((msg) =>
                msg.id === assistantMessageId || msg.streaming ? { ...msg, content } : msg
              )
            )
          },
          onComplete: (response) => {
            setBusy(false)
            setMessages((prev) =>
              prev.map((msg) =>
                msg.id === assistantMessageId || msg.streaming
                  ? {
                      id: response.conversation_id ? `stream-${Date.now()}` : assistantMessageId,
                      role: 'assistant',
                      content: response.content,
                      actions: response.actions || [],
                      streaming: false
                    }
                  : msg
              )
            )
            if (response.conversation_id && response.conversation_id !== conversationId) {
              navigate(`/chat/${response.conversation_id}`, { replace: true })
            }
            window.dispatchEvent(new Event(conversationRefreshEvent))
          },
          onError: (error) => {
            setBusy(false)
            setMessages((prev) =>
              prev.map((msg) =>
                msg.id === assistantMessageId || msg.streaming
                  ? { ...msg, content: msg.content || `Error: ${error}`, error: true, streaming: false }
                  : msg
              )
            )
          },
          onStopped: () => {
            setBusy(false)
            setMessages((prev) =>
              prev.map((msg) =>
                msg.id === assistantMessageId || msg.streaming
                  ? { ...msg, streaming: false }
                  : msg
              )
            )
          },
          onInactive: () => {
            setBusy(false)
          },
          onConversationId: (newId) => {
            // Navigate to the new/existing conversation URL
            if (newId !== conversationId) {
              navigate(`/chat/${newId}`, { replace: true })
            }
          }
        }
      )

    } catch (err) {
      setBusy(false)
      setMessages((prev) =>
        prev.map((msg) =>
          msg.streaming
            ? { ...msg, content: msg.content || `Error: ${err.message}`, error: true, streaming: false }
            : msg
        )
      )
    }
  }

  return (
    <div className="chat-container">
      <div className="chat-messages">
        {messages.length === 0 ? (
          <div className="chat-empty-state">
            <p className="chat-empty-title">How can I help with your Gmail?</p>
            <p className="chat-empty-copy">Ask me to organise messages, build filters, or clean up clutter.</p>
          </div>
        ) : (
          messages.map((msg, idx) => (
            <div key={msg.id || idx} className={`message ${msg.role}`}>
              <div className="message-avatar">
                {msg.role === 'user' ? (
                  profilePicture ? (
                    <img className="message-avatar-image" src={profilePicture} alt="You" />
                  ) : (
                    'You'
                  )
                ) : (
                  'AI'
                )}
              </div>
              <div className={`message-content${msg.error ? ' error' : ''}`}>
                {msg.role === 'assistant' ? (
                  <div className="markdown-content">
                    <MarkdownMessage content={msg.content} />
                  </div>
                ) : (
                  <div className="message-text">{msg.content}</div>
                )}
                {msg.actions && msg.actions.length > 0 ? (
                  <div className="message-actions">
                    {msg.actions.map((action, actionIndex) => (
                      <div key={`${msg.id}-action-${actionIndex}`} className="message-action-item">
                        {action}
                      </div>
                    ))}
                  </div>
                ) : null}
                {msg.streaming ? (
                  <div className="streaming-indicator">
                    <span className="streaming-dot" />
                    <span className="streaming-dot" />
                    <span className="streaming-dot" />
                  </div>
                ) : null}
              </div>
            </div>
          ))
        )}
        <div ref={messagesEndRef} />
      </div>

      <form className="chat-form" onSubmit={handleSubmit}>
        <div className="chat-input-bar">
          <textarea
            ref={textareaRef}
            className="chat-input"
            value={input}
            onChange={(inputEvent) => setInput(inputEvent.target.value)}
            placeholder="Ask..."
            rows={1}
            onKeyDown={(keyEvent) => {
              if (keyEvent.key === 'Enter' && !keyEvent.shiftKey) {
                keyEvent.preventDefault()
                handleSubmit(keyEvent)
              }
            }}
          />
        </div>
        {busy ? (
          <button type="button" className="send-button stop-button" onClick={handleStop} aria-label="Stop response">
            <svg width="18" height="18" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
              <rect x="7" y="7" width="10" height="10" rx="2" />
            </svg>
          </button>
        ) : (
          <button type="submit" className="send-button" disabled={!input.trim()} aria-label="Send message">
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <path d="M22 2 11 13" />
              <path d="m22 2-7 20-4-9-9-4Z" />
            </svg>
          </button>
        )}
      </form>
    </div>
  )
}

export default Chat
