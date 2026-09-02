import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  server: {
    port: 3000,
    proxy: {
      '/api': {
        target: 'http://192.168.15.201:8071',
        changeOrigin: true
      },
      '/mqtt': {
        target: 'ws://192.168.15.201:8071',
        changeOrigin: true,
        ws: true
      }
    }
  }
})
