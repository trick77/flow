import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: { proxy: { "/api": "http://127.0.0.1:8080" } },
  build: {
    outDir: "../backend/web/dist",
    // ship empties the dir but for its .gitkeep before the build; vite's own
    // emptying would take the .gitkeep with it.
    emptyOutDir: false,
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    // Tests assert local times; CI runs in UTC.
    env: { TZ: "Europe/Zurich" },
    coverage: {
      provider: "v8",
      // lcov is what ship check reads (the UI floor and the patch gate; its
      // ui.test passes the reporters itself); text-summary is for humans.
      reporter: ["text-summary", "lcov"],
      reportsDirectory: "../coverage/ui",
      include: ["src/**/*.{ts,tsx}"],
      exclude: [
        "src/**/*.test.{ts,tsx}",
        "src/**/*.d.ts",
        "src/main.tsx",
        "src/test/**",
      ],
    },
  },
});
