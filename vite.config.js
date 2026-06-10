import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// Proxy API requests to Go backend on port 8080
export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:8085',
        changeOrigin: true,
        secure: false
      }
    }
  }
})
