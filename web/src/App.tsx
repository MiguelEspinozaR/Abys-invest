import { Route, Routes } from 'react-router-dom';
import Layout from './components/Layout';
import WatchlistPage from './pages/WatchlistPage';
import DashboardPage from './pages/DashboardPage';
import HealthPage from './pages/HealthPage';
import TickerDetailPage from './pages/TickerDetailPage';

// Rutas del dashboard M4. Las alertas (/alerts) NO existen en M4: se diferieron
// a M5 (plan §0, desviación de alcance CA M4-2). M5 añade /watchlist (página
// dedicada) y M5.2 /health (página de salud del sistema).
//
// M5.2 (decisión D7): las 4 rutas cuelgan de una RUTA PADRE con <Layout/> — el
// sidebar con secciones "Finanzas" (Dashboard, Watchlist) y "Sistema" (Health) y
// el fondo/tema slate que antes estaba duplicado en cada página. El listado de
// rutas NO cambia: son las mismas 4.
export default function App() {
  return (
    <Routes>
      <Route element={<Layout />}>
        <Route path="/" element={<DashboardPage />} />
        <Route path="/ticker/:ticker" element={<TickerDetailPage />} />
        <Route path="/watchlist" element={<WatchlistPage />} />
        <Route path="/health" element={<HealthPage />} />
      </Route>
    </Routes>
  );
}
