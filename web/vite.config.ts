import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// The API is proxied in dev so `npm run dev` works beside `continuum serve`.
export default defineConfig({
  plugins: [react()],
  build: { outDir: "dist", emptyOutDir: true },
  server: { proxy: { "/api": "http://127.0.0.1:8088" } },
  test: { environment: "node", include: ["test/**/*.test.{ts,tsx}"] },
});
