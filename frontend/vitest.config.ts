import { defineConfig } from "vitest/config";
import { svelte } from "@sveltejs/vite-plugin-svelte";

// Separate from vite.config.ts: the Wails Vite plugin requires generated
// bindings, which tests do not need (the api module is mocked).
export default defineConfig({
  plugins: [svelte()],
  resolve: process.env.VITEST ? { conditions: ["browser"] } : undefined,
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.ts"],
    setupFiles: ["src/test-setup.ts"],
  },
});
