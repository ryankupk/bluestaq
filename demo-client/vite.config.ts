import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The backend has no CORS on purpose, so proxy /api (including the WebSocket)
// and the browser only ever sees one origin.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: { '/api': { target: 'http://localhost:8080', ws: true } },
  },
})
