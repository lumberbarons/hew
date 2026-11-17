import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vitest/config';

// The daemon embeds dist/ with go:embed, which needs the directory to exist
// and be non-empty even before the UI is built. Vite empties the output
// directory, so this puts the placeholder back after every build.
const keepDist = () => ({
  name: 'keep-dist',
  closeBundle() {
    mkdirSync('../dist', { recursive: true });
    writeFileSync(join('../dist', '.gitkeep'), '');
  },
});

// `npm run dev` proxies the API to a running daemon. The daemon refuses a
// request whose Origin is not its own Host (its CSRF guard), so the proxy must
// send both as the daemon's own: changeOrigin rewrites Host to the target, and
// the Origin header is set to match. Setting only Origin leaves Host as the dev
// server's, and every request is refused.
const daemon = process.env.HEW_DAEMON ?? 'http://127.0.0.1:7777';

export default defineConfig({
  plugins: [react(), keepDist()],
  build: { outDir: '../dist', emptyOutDir: true },
  server: {
    proxy: { '/api': { target: daemon, changeOrigin: true, headers: { Origin: daemon } } },
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
    coverage: {
      provider: 'v8',
      include: ['src/**/*.{ts,tsx}'],
      exclude: [
        'src/**/*.test.{ts,tsx}',
        'src/test/**',
        'src/main.tsx',
        'src/fixtures/**',
        'src/**/*.d.ts',
      ],
      reporter: ['text', 'text-summary'],
      thresholds: { statements: 90, lines: 90, functions: 90, branches: 85 },
    },
  },
});
