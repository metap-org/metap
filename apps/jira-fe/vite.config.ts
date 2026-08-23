import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Port 5174, not crm-fe's default 5173 — the two frontends run side by side against their own
// backends (jira-server on 3100, crm-server on 3000), same "different port so both can run
// together" reasoning apps/jira-server/.env.example already applies on the backend side.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5174,
    proxy: {
      "/api": "http://localhost:3100",
      "/metadata": "http://localhost:3100",
      "/health": "http://localhost:3100",
      "/preferences": "http://localhost:3100",
      "/auth": "http://localhost:3100",
      "/admin": "http://localhost:3100",
    },
  },
});
