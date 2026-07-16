import { formatPrice } from "../utils/pricing.js";

const CHART_WIDTH = 280;
const CHART_HEIGHT = 64;
const BAR_GAP = 2;
const MIN_BAR_HEIGHT = 16;

function toMinutes(time) {
  const [h, m] = (time || "00:00").split(":").map(Number);
  return (h || 0) * 60 + (m || 0);
}

function minutesToTime(min) {
  const h = Math.floor(min / 60)
    .toString()
    .padStart(2, "0");
  const m = (min % 60).toString().padStart(2, "0");
  return `${h}:${m}`;
}

/**
 * Small inline bar chart: one bar per pricing window, width proportional
 * to its duration in the day, height/label proportional to its price.
 * Single series (one hue, no legend needed) — only rendered when a tariff
 * actually has more than one window (see utils/pricing.js#hasHourlyPricing);
 * a flat price is shown as plain text instead.
 */
export default function HourlyPriceChart({ tariff, priceMode, chargeKWh }) {
  const windows = tariff?.extra?.windows ?? [];
  const priced = windows.filter((w) => w.energyPriceCentsPerKwh != null);
  if (priced.length < 2) return null;

  const prices = priced.map((w) => w.energyPriceCentsPerKwh);
  const minPrice = Math.min(...prices);
  const maxPrice = Math.max(...prices);
  const priceRange = maxPrice - minPrice || 1;

  const bars = priced.flatMap((w) => {
    const startMin = toMinutes(w.startTime);
    const endMin = toMinutes(w.endTime);
    // A window that wraps past midnight (e.g. 23:00-04:00) applies to two
    // separate stretches of the 0-24h chart at the same price: render both
    // segments instead of dropping the [00:00, endMin) portion.
    const segments =
      endMin > startMin ? [[startMin, endMin]] : [[startMin, 24 * 60], ...(endMin > 0 ? [[0, endMin]] : [])];

    return segments.map(([segStart, segEnd]) => {
      const x = (segStart / 1440) * CHART_WIDTH;
      const width = Math.max(((segEnd - segStart) / 1440) * CHART_WIDTH - BAR_GAP, 1);
      const heightRatio = MIN_BAR_HEIGHT / CHART_HEIGHT + (1 - MIN_BAR_HEIGHT / CHART_HEIGHT) * ((w.energyPriceCentsPerKwh - minPrice) / priceRange);
      const height = heightRatio * CHART_HEIGHT;
      return { ...w, startTime: minutesToTime(segStart), x, width, height };
    });
  });

  return (
    <figure className="hourly-price-chart">
      <figcaption>Prix par créneau horaire</figcaption>
      <svg
        viewBox={`0 0 ${CHART_WIDTH} ${CHART_HEIGHT + 14}`}
        role="img"
        aria-label={`Prix par créneau horaire, de ${formatPrice(minPrice, priceMode, chargeKWh)} à ${formatPrice(maxPrice, priceMode, chargeKWh)}`}
      >
        <line x1={0} y1={CHART_HEIGHT} x2={CHART_WIDTH} y2={CHART_HEIGHT} className="hourly-price-chart-baseline" />
        {bars.map((bar, i) => (
          <g key={i}>
            <rect
              x={bar.x}
              y={CHART_HEIGHT - bar.height}
              width={bar.width}
              height={bar.height}
              rx={4}
              className="hourly-price-chart-bar"
            />
            <text x={bar.x + bar.width / 2} y={CHART_HEIGHT - bar.height - 4} textAnchor="middle" className="hourly-price-chart-value">
              {(bar.energyPriceCentsPerKwh / 100).toFixed(2)}
            </text>
            <text x={bar.x + bar.width / 2} y={CHART_HEIGHT + 12} textAnchor="middle" className="hourly-price-chart-label">
              {bar.startTime}
            </text>
          </g>
        ))}
      </svg>
      <table className="visually-hidden">
        <caption>Prix par créneau horaire pour ce tarif</caption>
        <thead>
          <tr>
            <th scope="col">Créneau</th>
            <th scope="col">Prix</th>
          </tr>
        </thead>
        <tbody>
          {priced.map((w, i) => (
            <tr key={i}>
              <td>
                {w.startTime}–{w.endTime}
              </td>
              <td>{formatPrice(w.energyPriceCentsPerKwh, priceMode, chargeKWh)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </figure>
  );
}
