import path from "node:path";
import { defineConfig, searchForWorkspaceRoot } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  resolve: {
    // `@metap/ui` (`link:../../../design-system`) is a real symlink to a sibling repo with its
    // own independent `pnpm install` — it has its own physical `react`/`react-dom` copy in its
    // own `node_modules` (same version, different module instance), so anything inside its
    // built `dist/` that calls a hook (`@radix-ui/react-tooltip`'s `TooltipProvider`, etc.)
    // resolves `react` against *that* copy instead of this app's, and crashes with "Invalid
    // hook call" / "more than one copy of React". `dedupe` forces every `react`/`react-dom`
    // import anywhere in the graph — including from within a linked package — to resolve to
    // this app's own copy instead of the nearest one on disk.
    dedupe: ["react", "react-dom"],
  },
  server: {
    // `@metap/platform-ui` (`link:../../../platform-ui`) and `@metap/ui`
    // (`link:../../../design-system`) are both real symlinks to sibling repos *outside* this
    // pnpm workspace, so Vite's default `fs.allow` (workspace root only) 403s every request for
    // their files through `/@fs/...` — platform-ui's raw TS source (consumed unbundled, see its
    // README) and design-system's built `dist/style.css` alike. Allow both directories
    // explicitly alongside the default workspace root.
    fs: {
      allow: [
        searchForWorkspaceRoot(process.cwd()),
        path.resolve(import.meta.dirname, "../../../platform-ui"),
        path.resolve(import.meta.dirname, "../../../design-system"),
      ],
    },
    proxy: {
      "/api": "http://localhost:3000",
      "/metadata": "http://localhost:3000",
      "/health": "http://localhost:3000",
      "/preferences": "http://localhost:3000",
      "/auth": "http://localhost:3000",
      "/admin": "http://localhost:3000",
    },
  },
});
