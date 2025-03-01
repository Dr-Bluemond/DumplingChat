import { fileURLToPath, URL } from 'node:url'

import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import vueDevTools from 'vite-plugin-vue-devtools'

// https://vite.dev/config/
export default defineConfig({
  plugins: [
    vue(),
    vueDevTools(),
  ],
  server: {
    proxy: {
      '/api': {
        target: 'http://10.192.1.254:8008', 
        changeOrigin: true,
      },
      '/ws': {
        target: 'ws://10.192.1.254:8008',
        changeOrigin: true,
        ws: true,
        headers: {
          'Origin': 'http://10.192.1.254:8008',
        }
      },
    },
  },
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url))
    },
  },
  base: process.env.NODE_ENV === 'production' ? '/static/' : '/',
  
})
