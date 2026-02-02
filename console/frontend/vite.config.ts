import { defineConfig, loadEnv } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import fs from 'fs'
import path from 'path'

// https://vitejs.dev/config/
export default defineConfig(({ mode }) => {
  // Load env file based on mode
  const env = loadEnv(mode, process.cwd(), '')
  
  // Server configuration from environment
  const serverHost = env.VITE_DEV_HOST || '127.0.0.1'
  const serverPort = parseInt(env.VITE_DEV_PORT || '5173', 10)
  const apiTarget = env.VITE_API_TARGET || 'http://127.0.0.1:8080'
  
  // TLS configuration
  const tlsEnabled = env.VITE_TLS_ENABLED === 'true'
  const tlsCertFile = env.VITE_TLS_CERT || ''
  const tlsKeyFile = env.VITE_TLS_KEY || ''
  
  // Build HTTPS config if TLS is enabled
  let httpsConfig: boolean | { key: Buffer; cert: Buffer } = false
  if (tlsEnabled && tlsCertFile && tlsKeyFile) {
    try {
      httpsConfig = {
        key: fs.readFileSync(path.resolve(tlsKeyFile)),
        cert: fs.readFileSync(path.resolve(tlsCertFile)),
      }
    } catch (e) {
      console.warn('Failed to load TLS certificates:', e)
    }
  }

  return {
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
      host: serverHost,
      port: serverPort,
      https: httpsConfig,
      proxy: {
        '/api': {
          target: apiTarget,
          changeOrigin: true,
          secure: false, // Allow self-signed certs in dev
        },
        '/health': {
          target: apiTarget,
          changeOrigin: true,
          secure: false,
        },
      },
    },
    
    preview: {
      host: serverHost,
      port: serverPort + 1000, // Preview on different port
      https: httpsConfig,
    },
  }
})
