import { HashRouter, Routes, Route, Link } from "react-router-dom";
import MapPage from "./pages/MapPage.jsx";
import AboutPage from "./pages/AboutPage.jsx";

export default function App() {
  return (
    <HashRouter>
      <div className="app-shell">
        <header className="app-header">
          <Link to="/" className="brand">
            OpenCharge
          </Link>
          <nav>
            <Link to="/about">À propos</Link>
          </nav>
        </header>
        <div className="app-body">
          <Routes>
            <Route path="/" element={<MapPage />} />
            <Route path="/station/:id" element={<MapPage />} />
            <Route path="/about" element={<AboutPage />} />
          </Routes>
        </div>
      </div>
    </HashRouter>
  );
}
