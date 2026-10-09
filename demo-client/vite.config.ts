import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The backend has no CORS config on purpose: the proxy makes every request, including
// the WebSocket upgrade, look same-origin to it (changeOrigin plus an Origin rewrite),
// so the page can be opened from any host, e.g. http://192.168.1.114:5173.
const backend = 'http://localhost:8080'

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': {
        target: backend,
        ws: true,
        changeOrigin: true,
        configure: (proxy) => {
          proxy.on('proxyReq', (proxyReq) => proxyReq.setHeader('origin', backend))
          proxy.on('proxyReqWs', (proxyReq) => proxyReq.setHeader('origin', backend))
        },
      },
    },
  },
})
