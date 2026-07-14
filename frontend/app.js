const API_DEFAULT = 'http://localhost:8080';
const LIMIT = 500;

const state = {
  apiBase: API_DEFAULT,
  stations: [],
  filtered: [],
  operators: [],
  markers: [],
};

const els = {
  apiBase: document.getElementById('apiBase'),
  operatorFilter: document.getElementById('operatorFilter'),
  cityFilter: document.getElementById('cityFilter'),
  refreshBtn: document.getElementById('refreshBtn'),
  resetBtn: document.getElementById('resetBtn'),
  status: document.getElementById('status'),
  stats: document.getElementById('stats'),
  stationList: document.getElementById('stationList'),
  visibleCount: document.getElementById('visibleCount'),
};

const map = L.map('map', { zoomControl: true }).setView([46.8, 2.2], 6);
L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
  maxZoom: 19,
  attribution: '&copy; OpenStreetMap contributors',
}).addTo(map);

const markersLayer = L.layerGroup().addTo(map);

function setStatus(message) {
  els.status.textContent = message;
}

function money(value) {
  if (value == null) return 'n/c';
  const euro = Number(value) / 100;
  if (Number.isNaN(euro)) return 'n/c';
  return `${euro.toFixed(2)} €`;
}

function stationPrice(station) {
  if (station.bestPriceCentsPerKwh != null) return money(station.bestPriceCentsPerKwh);
  if (station.pricing?.bestPriceCentsPerKwh != null) return money(station.pricing.bestPriceCentsPerKwh);
  return 'n/c';
}

function formatAddress(station) {
  const parts = [station.address?.street, station.address?.postalCode, station.address?.city].filter(Boolean);
  return parts.join(' · ');
}

function hasCoordinates(station) {
  return typeof station.location?.lat === 'number' && typeof station.location?.lng === 'number';
}

function clearMap() {
  markersLayer.clearLayers();
  state.markers = [];
}

function renderStats() {
  const count = state.filtered.length;
  const withPrice = state.filtered.filter((station) => station.bestPriceCentsPerKwh != null || station.pricing?.bestPriceCentsPerKwh != null).length;
  const operators = new Set(state.filtered.map((station) => station.operator).filter(Boolean)).size;

  els.stats.innerHTML = [
    { label: 'Stations', value: count },
    { label: 'Tarif connu', value: withPrice },
    { label: 'Opérateurs', value: operators },
    { label: 'Total brut', value: state.stations.length },
  ]
    .map((item) => `<div class="stat"><span class="label">${item.label}</span><div class="value">${item.value}</div></div>`)
    .join('');

  els.visibleCount.textContent = String(count);
}

function stationPopupHtml(station) {
  const price = stationPrice(station);
  const connectors = (station.connectors || [])
    .map((connector) => `${connector.kind || connector.standard || 'conn'} ${connector.maxPowerKw ? `${connector.maxPowerKw} kW` : ''}`.trim())
    .filter(Boolean)
    .slice(0, 4)
    .join(' · ');

  return `
    <div style="min-width:220px">
      <strong>${station.name || 'Station'}</strong><br />
      <span>${station.operator || 'Opérateur inconnu'}</span><br />
      <span>${price} / kWh</span><br />
      <span>${formatAddress(station) || 'Adresse inconnue'}</span>
      ${connectors ? `<hr /><span>${connectors}</span>` : ''}
    </div>
  `;
}

function addMarkers(stations) {
  clearMap();
  const bounds = [];

  stations.forEach((station) => {
    if (!hasCoordinates(station)) return;
    const lat = station.location.lat;
    const lng = station.location.lng;
    bounds.push([lat, lng]);

    const color = station.bestPriceCentsPerKwh != null || station.pricing?.bestPriceCentsPerKwh != null ? '#0f766e' : '#8f96a3';
    const marker = L.circleMarker([lat, lng], {
      radius: 7,
      weight: 2,
      color,
      fillColor: color,
      fillOpacity: 0.88,
    })
      .bindPopup(stationPopupHtml(station), { maxWidth: 320 });

    marker.addTo(markersLayer);
  });

  if (bounds.length > 0) {
    map.fitBounds(bounds, { padding: [30, 30], maxZoom: 11 });
  }
}

function renderList() {
  if (!state.filtered.length) {
    els.stationList.innerHTML = '<div class="station-card"><h3>Aucune station</h3><div class="station-meta">Aucun résultat pour ces filtres.</div></div>';
    return;
  }

  els.stationList.innerHTML = state.filtered
    .slice(0, 150)
    .map((station, index) => {
      const badges = [];
      if (station.source) badges.push(station.source);
      if (station.status) badges.push(station.status);
      if (station.is24_7) badges.push('24/7');
      if (station.bestPriceCentsPerKwh != null || station.pricing?.bestPriceCentsPerKwh != null) {
        badges.push(`${stationPrice(station)} / kWh`);
      }
      return `
        <article class="station-card" data-index="${index}">
          <h3>${station.name || 'Station'}</h3>
          <div class="station-meta">
            ${station.operator || 'Opérateur inconnu'}<br />
            ${formatAddress(station) || 'Adresse inconnue'}
          </div>
          <div class="badge-row">${badges.map((badge) => `<span class="badge">${badge}</span>`).join('')}</div>
        </article>
      `;
    })
    .join('');

  els.stationList.querySelectorAll('.station-card').forEach((card) => {
    card.addEventListener('click', () => {
      const station = state.filtered[Number(card.dataset.index)];
      if (!station || !hasCoordinates(station)) return;
      map.setView([station.location.lat, station.location.lng], 13);
    });
  });
}

function applyFilters() {
  const operator = els.operatorFilter.value.trim();
  const city = els.cityFilter.value.trim().toLowerCase();

  state.filtered = state.stations.filter((station) => {
    const matchesOperator = !operator || station.operator === operator;
    const matchesCity = !city || `${station.address?.city || ''}`.toLowerCase().includes(city);
    return matchesOperator && matchesCity;
  });

  state.filtered.sort((a, b) => {
    const pa = a.bestPriceCentsPerKwh ?? a.pricing?.bestPriceCentsPerKwh ?? Number.POSITIVE_INFINITY;
    const pb = b.bestPriceCentsPerKwh ?? b.pricing?.bestPriceCentsPerKwh ?? Number.POSITIVE_INFINITY;
    return pa - pb;
  });

  renderStats();
  renderList();
  addMarkers(state.filtered);
}

async function loadOperators() {
  const response = await fetch(`${state.apiBase}/operators`);
  if (!response.ok) throw new Error(`operators HTTP ${response.status}`);
  const data = await response.json();
  state.operators = Array.isArray(data.operators) ? data.operators : [];

  const current = els.operatorFilter.value;
  els.operatorFilter.innerHTML = '<option value="">Tous les opérateurs</option>';
  state.operators.forEach((operator) => {
    const option = document.createElement('option');
    option.value = operator;
    option.textContent = operator;
    els.operatorFilter.appendChild(option);
  });
  els.operatorFilter.value = current;
}

async function loadStations() {
  const stations = [];
  let offset = 0;

  while (true) {
    const response = await fetch(`${state.apiBase}/stations?limit=${LIMIT}&offset=${offset}&sort=name`);
    if (!response.ok) throw new Error(`stations HTTP ${response.status}`);
    const data = await response.json();
    const chunk = Array.isArray(data.stations) ? data.stations : [];
    stations.push(...chunk);
    if (chunk.length < LIMIT) break;
    offset += LIMIT;
  }

  state.stations = stations;
}

async function refresh() {
  state.apiBase = els.apiBase.value.trim() || API_DEFAULT;
  setStatus('Chargement de l’API...');
  els.refreshBtn.disabled = true;
  try {
    await loadOperators();
    setStatus('Chargement des stations...');
    await loadStations();
    setStatus(`Données chargées depuis ${state.apiBase}`);
    applyFilters();
  } catch (error) {
    console.error(error);
    setStatus(`Erreur: ${error.message}`);
    els.stationList.innerHTML = '<div class="station-card"><h3>Erreur de chargement</h3><div class="station-meta">Vérifie que le backend est bien lancé sur le port 8080.</div></div>';
    renderStats();
    clearMap();
  } finally {
    els.refreshBtn.disabled = false;
  }
}

function resetFilters() {
  els.operatorFilter.value = '';
  els.cityFilter.value = '';
  applyFilters();
}

els.operatorFilter.addEventListener('change', applyFilters);
els.cityFilter.addEventListener('input', applyFilters);
els.refreshBtn.addEventListener('click', refresh);
els.resetBtn.addEventListener('click', resetFilters);
els.apiBase.addEventListener('change', refresh);

refresh();
