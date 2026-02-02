import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [svelte()],
  
  // 针对 Qubes OS 轻量化优化
  build: {
    // 减小 bundle 大小
    minify: 'esbuild',
    // 拆分代码
    rollupOptions: {
      output: {
        manualChunks: {
          vendor: ['svelte'],
        },
      },
    },
    // 目标现代浏览器
    target: 'esnext',
  },
  
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
      },
    },
  },
})
