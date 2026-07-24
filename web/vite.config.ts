import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// The SPA builds to web/dist, which is embedded into the Go backend (web/embed.go)
// and served from the same origin as the API. In `npm run dev`, /api and /agent
// are proxied to the running backend so the dev server and Go backend cooperate.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      "/api": "http://localhost:8080",
      "/agent": "http://localhost:8080",
      "/healthz": "http://localhost:8080",
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
