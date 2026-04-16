const apiBaseUrl = import.meta.env.VITE_API_BASE_URL || `http://127.0.0.1:8000`

class StreamManager {
  constructor() {
    this.connections = new Map()
    this.listeners = new Map()
  }

  subscribe(conversationId, callbacks) {
    if (!this.listeners.has(conversationId)) {
      this.listeners.set(conversationId, new Set())
    }
    this.listeners.get(conversationId).add(callbacks)

    // If there's an active connection, immediately send current state
    const connection = this.connections.get(conversationId)
    if (connection) {
      if (connection.currentContent && callbacks.onContent) {
        callbacks.onContent(connection.currentContent)
      }
      if (connection.isComplete && callbacks.onComplete) {
        callbacks.onComplete(connection.finalResponse)
      }
      if (connection.hasError && callbacks.onError) {
        callbacks.onError(connection.errorMessage)
      }
      if (connection.isActive && callbacks.onActive) {
        callbacks.onActive()
      }
    }

    return () => {
      const listeners = this.listeners.get(conversationId)
      if (listeners) {
        listeners.delete(callbacks)
      }
    }
  }

  notifyListeners(conversationId, event, data) {
    const listeners = this.listeners.get(conversationId)
    if (!listeners) return

    listeners.forEach(callbacks => {
      switch (event) {
        case 'token':
          if (callbacks.onToken) callbacks.onToken(data)
          break
        case 'content':
          if (callbacks.onContent) callbacks.onContent(data)
          break
        case 'complete':
          if (callbacks.onComplete) callbacks.onComplete(data)
          break
        case 'error':
          if (callbacks.onError) callbacks.onError(data)
          break
        case 'active':
          if (callbacks.onActive) callbacks.onActive()
          break
        case 'inactive':
          if (callbacks.onInactive) callbacks.onInactive()
          break
        case 'stopped':
          if (callbacks.onStopped) callbacks.onStopped(data)
          break
      }
    })
  }

  async startStream(conversationId, userMessage, callbacks = {}) {
    // Close any existing connection for this conversation
    this.closeConnection(conversationId)

    const connection = {
      source: null,
      isActive: true,
      isComplete: false,
      hasError: false,
      currentContent: '',
      finalResponse: null,
      errorMessage: '',
      lastEventId: 0,
      sessionId: null,
      isStopped: false
    }
    this.connections.set(conversationId, connection)

    try {
      // Start session
      const sessionPayload = { message: userMessage }
      if (conversationId) {
        sessionPayload.conversation_id = conversationId
      }

      const sessionResponse = await fetch(`${apiBaseUrl}/api/chat/session`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(sessionPayload)
      })

      if (!sessionResponse.ok) {
        const error = await sessionResponse.json().catch(() => ({ detail: 'Failed to start session' }))
        throw new Error(error.detail || 'Failed to start session')
      }

      const session = await sessionResponse.json()
      const resolvedConversationId = session.conversation_id
      const sessionId = session.session_id || resolvedConversationId
      connection.sessionId = sessionId

      // If we got a new conversation ID, update our tracking
      if (resolvedConversationId !== conversationId) {
        this.closeConnection(conversationId)
        this.connections.set(resolvedConversationId, connection)
        // Notify about new conversation ID
        if (callbacks.onConversationId) {
          callbacks.onConversationId(resolvedConversationId)
        }
      }

      // Connect to SSE stream
      const streamUrl = `${apiBaseUrl}/api/chat/stream?session_id=${encodeURIComponent(sessionId)}`
      const source = new EventSource(streamUrl)
      connection.source = source

      source.addEventListener('token', (streamEvent) => {
        try {
          const payload = JSON.parse(streamEvent.data)
          const token = typeof payload?.v === 'string' ? payload.v : ''
          if (token) {
            connection.currentContent += token
            this.notifyListeners(resolvedConversationId, 'token', token)
            this.notifyListeners(resolvedConversationId, 'content', connection.currentContent)
          }
          if (streamEvent.lastEventId) {
            connection.lastEventId = parseInt(streamEvent.lastEventId, 10)
          }
        } catch {}
      })

      source.addEventListener('done', (streamEvent) => {
        try {
          const payload = JSON.parse(streamEvent.data)
          connection.isActive = false
          connection.isComplete = true
          connection.finalResponse = {
            content: payload.response || connection.currentContent,
            actions: payload.actions || [],
            conversation_id: payload.conversation_id
          }
          this.notifyListeners(resolvedConversationId, 'complete', connection.finalResponse)
        } catch {}
        this.closeConnection(resolvedConversationId)
      })

      source.addEventListener('failed', (streamEvent) => {
        try {
          const payload = JSON.parse(streamEvent.data)
          connection.isActive = false
          connection.hasError = true
          connection.errorMessage = payload.detail || 'Stream failed'
          this.notifyListeners(resolvedConversationId, 'error', connection.errorMessage)
        } catch {}
        this.closeConnection(resolvedConversationId)
      })

      source.addEventListener('stopped', (streamEvent) => {
        try {
          const payload = JSON.parse(streamEvent.data)
          connection.isActive = false
          connection.isStopped = true
          connection.errorMessage = payload.detail || 'Stopped'
          this.notifyListeners(resolvedConversationId, 'stopped', connection.errorMessage)
        } catch {}
        this.closeConnection(resolvedConversationId)
      })

      source.onerror = () => {
        if (source.readyState === EventSource.CLOSED) {
          connection.isActive = false
          this.notifyListeners(resolvedConversationId, 'inactive')
        }
      }

      this.notifyListeners(resolvedConversationId, 'active')
      return resolvedConversationId

    } catch (err) {
      connection.isActive = false
      connection.hasError = true
      connection.errorMessage = err.message
      this.notifyListeners(conversationId, 'error', err.message)
      this.closeConnection(conversationId)
      throw err
    }
  }

  async reconnectToStream(conversationId, callbacks = {}) {
    // Check if there's an existing active stream
    const existingConnection = this.connections.get(conversationId)
    if (existingConnection && existingConnection.isActive) {
      // Already connected, just subscribe to updates
      return this.subscribe(conversationId, callbacks)
    }

    // Check if there's a stream session on the backend
    try {
      const response = await fetch(`${apiBaseUrl}/api/chat/stream/state?session_id=${encodeURIComponent(conversationId)}`)
      if (!response.ok) {
        // No active stream session
        return null
      }

      const state = await response.json()

      // If stream is already completed or errored, don't reconnect
      if (state.completed || state.has_error) {
        return null
      }

      // There's an active stream, reconnect to it
      const connection = {
        source: null,
        isActive: true,
        isComplete: false,
        hasError: false,
        currentContent: state.content || '',
        finalResponse: null,
        errorMessage: '',
        lastEventId: state.last_event_id || 0,
        sessionId: conversationId,
        isStopped: false
      }
      this.connections.set(conversationId, connection)

      // Connect to SSE stream with last_event_id for resumption
      const streamUrl = `${apiBaseUrl}/api/chat/stream?session_id=${encodeURIComponent(conversationId)}&last_event_id=${connection.lastEventId}`
      const source = new EventSource(streamUrl)
      connection.source = source

      // Immediately notify of current accumulated content
      if (connection.currentContent) {
        this.notifyListeners(conversationId, 'content', connection.currentContent)
      }

      source.addEventListener('token', (streamEvent) => {
        try {
          const payload = JSON.parse(streamEvent.data)
          const token = typeof payload?.v === 'string' ? payload.v : ''
          if (token) {
            connection.currentContent += token
            this.notifyListeners(conversationId, 'token', token)
            this.notifyListeners(conversationId, 'content', connection.currentContent)
          }
          if (streamEvent.lastEventId) {
            connection.lastEventId = parseInt(streamEvent.lastEventId, 10)
          }
        } catch {}
      })

      source.addEventListener('done', (streamEvent) => {
        try {
          const payload = JSON.parse(streamEvent.data)
          connection.isActive = false
          connection.isComplete = true
          connection.finalResponse = {
            content: payload.response || connection.currentContent,
            actions: payload.actions || [],
            conversation_id: payload.conversation_id
          }
          this.notifyListeners(conversationId, 'complete', connection.finalResponse)
        } catch {}
        this.closeConnection(conversationId)
      })

      source.addEventListener('failed', (streamEvent) => {
        try {
          const payload = JSON.parse(streamEvent.data)
          connection.isActive = false
          connection.hasError = true
          connection.errorMessage = payload.detail || 'Stream failed'
          this.notifyListeners(conversationId, 'error', connection.errorMessage)
        } catch {}
        this.closeConnection(conversationId)
      })

      source.addEventListener('stopped', (streamEvent) => {
        try {
          const payload = JSON.parse(streamEvent.data)
          connection.isActive = false
          connection.isStopped = true
          connection.errorMessage = payload.detail || 'Stopped'
          this.notifyListeners(conversationId, 'stopped', connection.errorMessage)
        } catch {}
        this.closeConnection(conversationId)
      })

      source.onerror = () => {
        if (source.readyState === EventSource.CLOSED) {
          connection.isActive = false
          this.notifyListeners(conversationId, 'inactive')
        }
      }

      this.notifyListeners(conversationId, 'active')

      // Return unsubscribe function
      return () => {
        const listeners = this.listeners.get(conversationId)
        if (listeners) {
          listeners.delete(callbacks)
        }
      }

    } catch (err) {
      // No active stream or error fetching state
      return null
    }
  }

  async stopStream(conversationId) {
    const connection = this.connections.get(conversationId)
    const sessionId = connection?.sessionId || conversationId
    if (!sessionId) {
      return false
    }

    const response = await fetch(`${apiBaseUrl}/api/chat/session/stop`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ session_id: sessionId })
    })

    if (!response.ok) {
      const error = await response.json().catch(() => ({ detail: 'Failed to stop session' }))
      throw new Error(error.detail || 'Failed to stop session')
    }

    return true
  }

  closeConnection(conversationId) {
    const connection = this.connections.get(conversationId)
    if (connection) {
      connection.isActive = false
      if (connection.source) {
        connection.source.close()
        connection.source = null
      }
      this.connections.delete(conversationId)
    }
  }

  getStreamState(conversationId) {
    const connection = this.connections.get(conversationId)
    if (!connection) return null

    return {
      isActive: connection.isActive,
      isComplete: connection.isComplete,
      hasError: connection.hasError,
      currentContent: connection.currentContent,
      finalResponse: connection.finalResponse,
      errorMessage: connection.errorMessage,
      sessionId: connection.sessionId,
      isStopped: connection.isStopped
    }
  }
}

// Singleton instance
const streamManager = new StreamManager()
export default streamManager
