import { writeFileSync } from 'node:fs'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    // emptyOutDir removes the committed dist/.gitkeep, which go:embed needs
    // so that `go test ./...` works before the UI is built.
    { name: 'keep-gitkeep', closeBundle: () => writeFileSync('dist/.gitkeep', '') },
  ],
  build: { outDir: 'dist', emptyOutDir: true, chunkSizeWarningLimit: 1500 },
  server: { port: 5173, proxy: { '/api': 'http://localhost:8080' } },
})
