import { lazy, Suspense, useCallback, useEffect, useState } from 'react'
import { BrowserRouter, Routes, Route, Navigate, useLocation, useParams } from 'react-router-dom'
import { ToastProvider } from './components/ToastProvider'

const Sidebar = lazy(() => import('./components/Sidebar'))
const GoogleOAuth = lazy(() => import('./pages/setup/GoogleOAuth'))
const GmailConnect = lazy(() => import('./pages/setup/GmailConnect'))
const LLMConfig = lazy(() => import('./pages/setup/LLMConfig'))
const Chat = lazy(() => import('./pages/Chat'))
const History = lazy(() => import('./pages/History'))
const Development = lazy(() => import('./pages/Development'))
const Preferences = lazy(() => import('./pages/Preferences'))
const ScheduledTasks = lazy(() => import('./pages/ScheduledTasks'))
const Settings = lazy(() => import('./pages/Settings'))

const apiBaseUrl = import.meta.env.VITE_API_BASE_URL || `http://localhost:8000`

function LoadingFallback() {
  return (
    <div className="page-shell loading-shell">
      <div className="loading-card">Loading Towel…</div>
    </div>
  )
}

function RequireSetupComplete({ status, onStatusChange }) {
  if (!status?.onboarding_completed) {
    return <Navigate to="/setup/google" replace />
  }

  return <AuthenticatedLayout status={status} onStatusChange={onStatusChange} />
}

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

function ChatWrapper({ status }) {
  const { conversationId } = useParams()
  return <Chat urlConversationId={conversationId || ''} status={status} />
}

function AuthenticatedLayout({ status, onStatusChange }) {
  const location = useLocation()
  const isChatPage = location.pathname.startsWith('/chat')

  return (
    <div className="app-layout">
      <Sidebar status={status} onStatusChange={onStatusChange} />
      <div className={`main-content${isChatPage ? ' chat-main-content' : ''}`}>
        <Routes>
          <Route path="/chat" element={<ChatWrapper status={status} />} />
          <Route path="/chat/:conversationId" element={<ChatWrapper status={status} />} />
          <Route path="/history" element={<History />} />
          <Route path="/development" element={<Development initialSyncStatus={status?.email_sync} onStatusChange={onStatusChange} />} />
          <Route path="/personalise" element={<Preferences />} />
          <Route path="/scheduled-tasks" element={<ScheduledTasks />} />
          <Route path="/settings" element={<Settings />} />
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
    return <LoadingFallback />
  }

  const isSetupComplete = status?.onboarding_completed

  return (
    <ToastProvider>
      <Suspense fallback={<LoadingFallback />}>
        <BrowserRouter>
          <Routes>
            <Route path="/setup/google" element={<GoogleOAuth onStatusChange={loadStatus} />} />
            <Route path="/setup/gmail" element={<GmailConnect onStatusChange={loadStatus} />} />
            <Route path="/setup/llm" element={<LLMConfig onStatusChange={loadStatus} />} />
            <Route path="/*" element={<RequireSetupComplete status={status} onStatusChange={loadStatus} />} />
          </Routes>
        </BrowserRouter>
      </Suspense>
    </ToastProvider>
  )
}

export default App
