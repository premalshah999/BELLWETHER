import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  return {
  plugins: [react(), tailwindcss()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    // The Go binary embeds this output; keep the asset folder name stable
    // because the server caches /assets/* immutably.
    assetsDir: "assets",
    sourcemap: false,
  },
  server: {
    port: 5173,
    proxy: {
      // In dev the Vite server fronts the Go API so the app is same-origin,
      // exactly as it is in production.
      "/api": {
        target: env.VITE_API_TARGET || "http://localhost:8080",
        changeOrigin: true,
      },
    },
  },
  };
});
