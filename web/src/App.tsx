import { Route, Routes } from 'react-router-dom';
import DashboardPage from './pages/DashboardPage';
import TickerDetailPage from './pages/TickerDetailPage';

// Rutas del dashboard M4. Las alertas (/alerts) NO existen en M4: se diferieron
// a M5 (plan §0, desviación de alcance CA M4-2).
export default function App() {
  return (
    <Routes>
      <Route path="/" element={<DashboardPage />} />
      <Route path="/ticker/:ticker" element={<TickerDetailPage />} />
    </Routes>
  );
}
