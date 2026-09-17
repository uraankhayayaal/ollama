import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Vite конфиг фронтенд (web/). Без node-only импортов — чистый Vite/React.
//  - dev: `npm run dev` → :5173, прокси /api и /events на backend :8090.
//  - build: `npm run build` → web/dist (embed в Go-бинар, прод same-origin).
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": new URL("./src", import.meta.url).pathname,
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      "/api": { target: "http://127.0.0.1:8090", changeOrigin: true, ws: true },
      "/events": { target: "http://127.0.0.1:8090", changeOrigin: true },
    },
  },
});
