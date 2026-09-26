import { Route, Routes } from 'react-router-dom';
import WatchlistPage from './pages/WatchlistPage';
import DashboardPage from './pages/DashboardPage';
import TickerDetailPage from './pages/TickerDetailPage';

// Rutas del dashboard M4. Las alertas (/alerts) NO existen en M4: se diferieron
// a M5 (plan §0, desviación de alcance CA M4-2). M5 añade /watchlist (página
// dedicada) — ver CA-M5-3.
export default function App() {
  return (
    <Routes>
      <Route path="/" element={<DashboardPage />} />
      <Route path="/ticker/:ticker" element={<TickerDetailPage />} />
      <Route path="/watchlist" element={<WatchlistPage />} />
    </Routes>
  );
}
