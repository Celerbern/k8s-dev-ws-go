import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// The built SPA is embedded into the Go binary (go:embed internal/web/dist).
export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: '../internal/web/dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
    },
  },
})