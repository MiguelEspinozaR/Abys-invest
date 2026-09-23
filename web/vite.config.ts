import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Target del proxy configurable (VITE_PROXY_TARGET). Default :8080 (plan M4
// D1/D3); para verificar contra un API en otro puerto, p.ej.:
//   VITE_PROXY_TARGET=http://localhost:18081 npm run dev -- --port 5176
// Ambient declaration: Vite ejecuta el config en Node, pero este tsconfig no
// incluye @types/node (se evita añadir dependencia solo para esto).
declare const process: { env: Record<string, string | undefined> };
const proxyTarget = process.env.VITE_PROXY_TARGET ?? 'http://localhost:8080';

// Dev: el proxy expone la API del Go (paths sin prefijo) bajo /api para
// mantener misma-origin y evitar CORS (plan M4, D1). El rewrite quita el
// prefijo porque las rutas del API se registran como /health, /securities...
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': {
        target: proxyTarget,
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/api/, ''),
      },
    },
  },
});
