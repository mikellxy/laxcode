import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, ".", "LAXCODE_");
  const backend = env.LAXCODE_PROXY_TARGET || "http://127.0.0.1:8090";

  return {
    plugins: [react()],
    server: {
      host: "127.0.0.1",
      port: 5173,
      strictPort: true,
      proxy: {
        "/api": backend,
        "/chat": backend,
        "/healthz": backend,
      },
    },
  };
});
