# Notes demo client

A minimal React app for demonstrating the notes service in two browser tabs. It is a demo aid, not part of the backend deliverable.

**Run it:** start the backend from the repo root (`docker compose up --build -d` or `make run`), then here run `pnpm install && pnpm dev` and open http://localhost:5173 in two tabs with different names.

The guided walkthrough, mapping each step to the server code it exercises, is in the main [README](../README.md#demo-client-see-it-working-in-two-browser-tabs).

**Code:** `src/App.tsx` is the UI and the conflict handling. `src/useLiveEvents.ts` is the WebSocket hook with reconnect. `src/api.ts` holds the typed fetch helpers that fetch a dev-mode token for your name, send it as a bearer token (re-fetching once on a 401), and send `If-Match`. `vite.config.ts` proxies `/api`, including the WebSocket, to `:8080` and rewrites `Origin` on the WebSocket upgrade, so the backend needs no CORS configuration for the demo.
