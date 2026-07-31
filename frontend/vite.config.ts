import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

const DEVELOPMENT_PORT = 5173;
const BACKEND_ORIGIN = "http://127.0.0.1:8080";

export default defineConfig({
  plugins: [react()],
  server: {
    port: DEVELOPMENT_PORT,
    strictPort: true,
    proxy: {
      "/api": BACKEND_ORIGIN,
    },
  },
});
