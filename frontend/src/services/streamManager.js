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
      if ((connection.currentContent || connection.currentActions.length) && callbacks.onProgress) {
        callbacks.onProgress({
          content: connection.currentContent,
          actions: connection.currentActions,
          conversation_id: connection.sessionId || conversationId,
          pending_user_message: connection.pendingUserMessage || ''
        })
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
        case 'progress':
          if (callbacks.onProgress) callbacks.onProgress(data)
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

  parseSSEEvent(chunk) {
    const lines = chunk.split('\n')
    let event = 'message'
    let id = ''
    const dataLines = []

    lines.forEach((line) => {
      if (!line || line.startsWith(':')) {
        return
      }
      if (line.startsWith('event:')) {
        event = line.slice(6).trim()
        return
      }
      if (line.startsWith('id:')) {
        id = line.slice(3).trim()
        return
      }
      if (line.startsWith('data:')) {
        dataLines.push(line.slice(5).trim())
      }
    })

    if (!dataLines.length) {
      return null
    }

    return {
      event,
      id,
      data: dataLines.join('\n')
    }
  }

  async consumeSSEStream(response, onEvent) {
    if (!response.body) {
      throw new Error('Streaming response body is not available')
    }

    const reader = response.body.getReader()
    const decoder = new TextDecoder()
    let buffer = ''

    while (true) {
      const { value, done } = await reader.read()
      if (done) {
        break
      }

      buffer += decoder.decode(value, { stream: true }).replace(/\r/g, '')

      let boundaryIndex = buffer.indexOf('\n\n')
      while (boundaryIndex >= 0) {
        const rawEvent = buffer.slice(0, boundaryIndex)
        buffer = buffer.slice(boundaryIndex + 2)
        const parsed = this.parseSSEEvent(rawEvent)
        if (parsed) {
          onEvent(parsed)
        }
        boundaryIndex = buffer.indexOf('\n\n')
      }
    }

    buffer += decoder.decode().replace(/\r/g, '')
    const trailingEvent = this.parseSSEEvent(buffer.trim())
    if (trailingEvent) {
      onEvent(trailingEvent)
    }
  }

  async openStreamConnection(conversationId, streamUrl, connection, callbacks = {}) {
    connection.abortController = new AbortController()

    try {
      const response = await fetch(streamUrl, {
        method: 'GET',
        headers: { Accept: 'text/event-stream' },
        signal: connection.abortController.signal
      })

      if (!response.ok) {
        const error = await response.json().catch(() => ({ detail: 'Failed to connect to stream' }))
        throw new Error(error.detail || 'Failed to connect to stream')
      }

      await this.consumeSSEStream(response, (streamEvent) => {
        if (streamEvent.id) {
          connection.lastEventId = parseInt(streamEvent.id, 10) || connection.lastEventId
        }

        if (streamEvent.event === 'token') {
          try {
            const payload = JSON.parse(streamEvent.data)
            const token = typeof payload?.v === 'string' ? payload.v : ''
            if (token) {
              connection.currentContent += token
              if (callbacks.onToken) callbacks.onToken(token)
              if (callbacks.onContent) callbacks.onContent(connection.currentContent)
              this.notifyListeners(conversationId, 'token', token)
              this.notifyListeners(conversationId, 'content', connection.currentContent)
            }
          } catch {}
          return
        }

        if (streamEvent.event === 'progress') {
          try {
            const payload = JSON.parse(streamEvent.data)
            connection.currentContent = payload.response || connection.currentContent
            connection.currentActions = Array.isArray(payload.actions) ? payload.actions : connection.currentActions
            const progress = {
              content: connection.currentContent,
              actions: connection.currentActions,
              conversation_id: payload.conversation_id,
              pending_user_message: connection.pendingUserMessage || ''
            }
            if (callbacks.onProgress) callbacks.onProgress(progress)
            if (callbacks.onContent) callbacks.onContent(connection.currentContent)
            this.notifyListeners(conversationId, 'progress', progress)
            this.notifyListeners(conversationId, 'content', connection.currentContent)
          } catch {}
          return
        }

        if (streamEvent.event === 'done') {
          try {
            const payload = JSON.parse(streamEvent.data)
            connection.isActive = false
            connection.isComplete = true
            connection.currentContent = payload.response || connection.currentContent
            connection.currentActions = Array.isArray(payload.actions) ? payload.actions : connection.currentActions
            connection.finalResponse = {
              content: connection.currentContent,
              actions: connection.currentActions,
              conversation_id: payload.conversation_id,
              pending_user_message: connection.pendingUserMessage || ''
            }
            if (callbacks.onContent) callbacks.onContent(connection.currentContent)
            if (callbacks.onComplete) callbacks.onComplete(connection.finalResponse)
            this.notifyListeners(conversationId, 'content', connection.currentContent)
            this.notifyListeners(conversationId, 'complete', connection.finalResponse)
          } catch {}
          this.closeConnection(conversationId)
          return
        }

        if (streamEvent.event === 'failed') {
          try {
            const payload = JSON.parse(streamEvent.data)
            connection.isActive = false
            connection.hasError = true
            connection.errorMessage = payload.detail || 'Stream failed'
            if (callbacks.onError) callbacks.onError(connection.errorMessage)
            this.notifyListeners(conversationId, 'error', connection.errorMessage)
          } catch {}
          this.closeConnection(conversationId)
          return
        }

        if (streamEvent.event === 'stopped') {
          try {
            const payload = JSON.parse(streamEvent.data)
            connection.isActive = false
            connection.isStopped = true
            connection.errorMessage = payload.detail || 'Stopped'
            if (callbacks.onStopped) callbacks.onStopped(connection.errorMessage)
            this.notifyListeners(conversationId, 'stopped', connection.errorMessage)
          } catch {}
          this.closeConnection(conversationId)
        }
      })

      if (this.connections.get(conversationId) === connection && connection.isActive) {
        connection.isActive = false
        if (callbacks.onInactive) callbacks.onInactive()
        this.notifyListeners(conversationId, 'inactive')
        this.closeConnection(conversationId)
      }
    } catch (err) {
      if (err?.name === 'AbortError') {
        return
      }

      connection.isActive = false
      connection.hasError = true
      connection.errorMessage = err.message
      if (callbacks.onError) callbacks.onError(err.message)
      this.notifyListeners(conversationId, 'error', err.message)
      this.closeConnection(conversationId)
    }
  }

  async fetchStreamState(conversationId) {
    try {
      const response = await fetch(`${apiBaseUrl}/api/chat/stream/state?session_id=${encodeURIComponent(conversationId)}`)
      if (!response.ok) {
        return null
      }
      return await response.json()
    } catch {
      return null
    }
  }

  async startStream(conversationId, userMessage, callbacks = {}) {
    // Close any existing connection for this conversation
    this.closeConnection(conversationId)

    const connection = {
      abortController: null,
      isActive: true,
      isComplete: false,
      hasError: false,
      currentContent: '',
      currentActions: [],
      pendingUserMessage: userMessage,
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
        this.connections.delete(conversationId)
        this.connections.set(resolvedConversationId, connection)
        // Move listeners to the new conversation ID so they continue receiving updates
        const oldListeners = this.listeners.get(conversationId)
        if (oldListeners) {
          this.listeners.set(resolvedConversationId, oldListeners)
          this.listeners.delete(conversationId)
        }
        // Notify about new conversation ID
        if (callbacks.onConversationId) {
          callbacks.onConversationId(resolvedConversationId)
        }
      }

      // Connect to SSE stream
      const streamUrl = `${apiBaseUrl}/api/chat/stream?session_id=${encodeURIComponent(sessionId)}`
      void this.openStreamConnection(resolvedConversationId, streamUrl, connection, callbacks)
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

  async reconnectToStream(conversationId, callbacks = {}, preloadedState = null) {
    // Check if there's an existing active stream
    const existingConnection = this.connections.get(conversationId)
    if (existingConnection && existingConnection.isActive) {
      // Already connected, just subscribe to updates
      return this.subscribe(conversationId, callbacks)
    }

    // Check if there's a stream session on the backend
    try {
      const state = preloadedState || await this.fetchStreamState(conversationId)
      if (!state) {
        return null
      }

      // If stream is already completed or errored, don't reconnect
      if (state.completed || state.has_error) {
        return null
      }

      // There's an active stream, reconnect to it
      const connection = {
        abortController: null,
        isActive: true,
        isComplete: false,
        hasError: false,
        currentContent: state.content || '',
        currentActions: state.actions || [],
        pendingUserMessage: '',
        finalResponse: null,
        errorMessage: '',
        lastEventId: state.last_event_id || 0,
        sessionId: conversationId,
        isStopped: false
      }
      this.connections.set(conversationId, connection)

      // Connect to SSE stream with last_event_id for resumption
      const streamUrl = `${apiBaseUrl}/api/chat/stream?session_id=${encodeURIComponent(conversationId)}&last_event_id=${connection.lastEventId}`
      void this.openStreamConnection(conversationId, streamUrl, connection, callbacks)
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
      if (connection.abortController) {
        connection.abortController.abort()
        connection.abortController = null
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
      currentActions: connection.currentActions,
      pendingUserMessage: connection.pendingUserMessage,
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
