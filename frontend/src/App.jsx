import { useCallback, useEffect, useState } from 'react'
import { BrowserRouter, Routes, Route, Navigate, useLocation, useParams } from 'react-router-dom'
import Sidebar from './components/Sidebar'
import { ToastProvider } from './components/ToastProvider'
import GoogleOAuth from './pages/setup/GoogleOAuth'
import GmailConnect from './pages/setup/GmailConnect'
import LLMConfig from './pages/setup/LLMConfig'
import Chat from './pages/Chat'
import History from './pages/History'
import Preferences from './pages/Preferences'

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

function ChatWrapper() {
  const { conversationId } = useParams()
  return <Chat urlConversationId={conversationId || ''} />
}

function AuthenticatedLayout({ status, onStatusChange }) {
  const location = useLocation()
  const isChatPage = location.pathname.startsWith('/chat')

  return (
    <div className="app-layout">
      <Sidebar status={status} onStatusChange={onStatusChange} />
      <div className={`main-content${isChatPage ? ' chat-main-content' : ''}`}>
        <Routes>
          <Route path="/chat" element={<ChatWrapper />} />
          <Route path="/chat/:conversationId" element={<ChatWrapper />} />
          <Route path="/history" element={<History />} />
          <Route path="/personalise" element={<Preferences />} />
          <Route path="/preferences" element={<Navigate to="/personalise" replace />} />
          <Route path="*" element={<Navigate to="/chat" replace />} />
        </Routes>
      </div>
    </div>
  )
}

function App() {
  const [status, setStatus] = useState(null)
  const [loading, setLoading] = useState(true)

  const loadStatus = useCallback(async () => {
    try {
      const response = await fetch(`${apiBaseUrl}/api/setup/status`)
      const data = await parseResponse(response)
      setStatus(data)
      return data
    } catch (err) {
      console.error('Failed to load status:', err)
      return null
    } finally {
      setLoading(false)
    }
  }, [apiBaseUrl])

  useEffect(() => {
    loadStatus()
  }, [loadStatus])

  if (loading) {
    return (
      <div className="page-shell loading-shell">
        <div className="loading-card">Loading Towel…</div>
      </div>
    )
  }

  const isSetupComplete = status?.onboarding_completed

  return (
    <ToastProvider>
      <BrowserRouter>
        {isSetupComplete ? (
          <AuthenticatedLayout status={status} onStatusChange={loadStatus} />
        ) : (
          <Routes>
            <Route path="/setup/google" element={<GoogleOAuth onStatusChange={loadStatus} />} />
            <Route path="/setup/gmail" element={<GmailConnect onStatusChange={loadStatus} />} />
            <Route path="/setup/llm" element={<LLMConfig onStatusChange={loadStatus} />} />
            <Route path="*" element={<Navigate to="/setup/google" replace />} />
          </Routes>
        )}
      </BrowserRouter>
    </ToastProvider>
  )
}

export default App
