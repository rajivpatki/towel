import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

const serviceWorkerMimeTypePlugin = () => ({
  name: 'service-worker-mime-type',
  configureServer(server) {
    server.middlewares.use((req, res, next) => {
      if (req.url === '/service-worker.js') {
        res.setHeader('Content-Type', 'application/javascript')
      }
      next()
    })
  }
})

export default defineConfig({
  plugins: [react(), serviceWorkerMimeTypePlugin()],
  server: {
    port: 3000,
    host: '0.0.0.0'
  }
})
