import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import './styles.css'
import './design-overrides.css'

async function removeLegacyServiceWorkers() {
  if (!('serviceWorker' in navigator)) {
    return
  }

  try {
    const registrations = await navigator.serviceWorker.getRegistrations()
    if (registrations.length === 0) {
      return
    }

    await Promise.all(registrations.map(registration => registration.unregister()))

    if ('caches' in window) {
      const cacheNames = await caches.keys()
      await Promise.all(cacheNames.map(cacheName => caches.delete(cacheName)))
    }

    // Reload once so the browser fetches a fresh index/chunk graph without a stale controller.
    if (navigator.serviceWorker.controller && sessionStorage.getItem('towel-sw-reset') !== '1') {
      sessionStorage.setItem('towel-sw-reset', '1')
      window.location.reload()
      return
    }

    sessionStorage.removeItem('towel-sw-reset')
    console.log('Removed legacy service worker registrations and caches')
  } catch (error) {
    console.error('Failed to remove legacy service workers:', error)
  }
}

removeLegacyServiceWorkers()

ReactDOM.createRoot(document.getElementById('root')).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
)
