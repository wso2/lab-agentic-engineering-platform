import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react-swc';

const apiTarget = process.env.API_PROXY_TARGET || 'http://localhost:9090';

export default defineConfig({
  plugins: [react()],
  resolve: {
    dedupe: ['react', 'react-dom'],
  },
  define: {
    global: 'globalThis',
    'process.env': {},
  },
  optimizeDeps: {
    include: ['@wso2/oxygen-ui-icons-react > lucide-react'],
  },
  server: {
    port: 8090,
    proxy: {
      '/asdlc-api-service': {
        target: apiTarget,
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/asdlc-api-service/, ''),
      },
    },
  },
});
