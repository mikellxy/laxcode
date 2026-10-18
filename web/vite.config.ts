import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, ".", "LAXCODE_");
  const backend = env.LAXCODE_PROXY_TARGET || "http://127.0.0.1:5173";

  return {
    plugins: [react()],
    server: {
      host: "127.0.0.1",
      port: 5174,
      strictPort: true,
      proxy: {
        "/api": backend,
        "/chat": backend,
        "/skills": backend,
        "/healthz": backend,
      },
    },
  };
});
