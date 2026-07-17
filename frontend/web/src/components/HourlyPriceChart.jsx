import { formatPrice } from "../utils/pricing.js";

const CHART_WIDTH = 280;
const CHART_HEIGHT = 64;
const BAR_GAP = 2;
const MIN_BAR_HEIGHT = 10;
const HOURS = 24;
const TICK_HOURS = [0, 6, 12, 18, 23];

// Mirrors backend/internal/ingestion/electra.go's timeInWindow: half-open
// [start, end) in HH:MM, wrapping past midnight (e.g. 22:00-06:00). Kept in
// sync by hand — same reason as utils/pricing.js's connector-kind sets,
// this can't import Go.
function timeInWindow(hm, start, end) {
  if (!start || !end) return false;
  if (start <= end) return hm >= start && hm < end;
  return hm >= start || hm < end;
}

// The price a tariff's windows say applies at a given hour of the day —
// always computed against the real current time at render time (never a
// value cached from the API), since a tariff's windows can span a whole
// day while the page can stay open for hours: only the browser's clock at
// render time can say which window is "now".
function priceAtHour(priced, hour) {
  const hm = `${String(hour).padStart(2, "0")}:00`;
  const match = priced.find((w) => timeInWindow(hm, w.startTime, w.endTime));
  return (match ?? priced[0])?.energyPriceCentsPerKwh ?? null;
}

/**
 * One bar per hour of the day (24 bars), height proportional to that
 * hour's price — a silhouette of the day's price curve rather than one bar
 * per raw pricing window, so a tariff with few wide windows and one with
 * many narrow ones read on the same visual scale. The bar for the current
 * hour (browser's local clock, matching the Europe/Paris assumption
 * ingestion already makes for window boundaries — see electra.go) is
 * highlighted; a flat price is shown as plain text instead (see
 * utils/pricing.js#hasHourlyPricing).
 */
export default function HourlyPriceChart({ tariff, priceMode, chargeKWh }) {
  const windows = tariff?.extra?.windows ?? [];
  const priced = windows.filter((w) => w.energyPriceCentsPerKwh != null);
  if (priced.length < 2) return null;

  const nowHour = new Date().getHours();
  const hourly = Array.from({ length: HOURS }, (_, h) => ({ hour: h, price: priceAtHour(priced, h) })).filter(
    (b) => b.price != null
  );
  if (hourly.length < 2) return null;

  const prices = hourly.map((b) => b.price);
  const minPrice = Math.min(...prices);
  const maxPrice = Math.max(...prices);
  const priceRange = maxPrice - minPrice || 1;

  const barWidth = CHART_WIDTH / HOURS - BAR_GAP;
  const minIdx = hourly.findIndex((b) => b.price === minPrice);
  const maxIdx = hourly.findIndex((b) => b.price === maxPrice);
  const nowIdx = hourly.findIndex((b) => b.hour === nowHour);
  // Only label a few bars (min, max, now) — labeling all 24 would be
  // unreadable at this width; priority order avoids two labels colliding
  // when they land on the same or adjacent bars.
  const labeledIdxs = new Set([minIdx, maxIdx, nowIdx].filter((i) => i >= 0));

  const bars = hourly.map((b, i) => {
    const x = i * (CHART_WIDTH / HOURS);
    const heightRatio = MIN_BAR_HEIGHT / CHART_HEIGHT + (1 - MIN_BAR_HEIGHT / CHART_HEIGHT) * ((b.price - minPrice) / priceRange);
    const height = heightRatio * CHART_HEIGHT;
    return { ...b, x, height, isNow: i === nowIdx, showLabel: labeledIdxs.has(i) };
  });

  return (
    <figure className="hourly-price-chart">
      <figcaption>Prix par heure · maintenant {String(nowHour).padStart(2, "0")}h</figcaption>
      <svg
        viewBox={`0 0 ${CHART_WIDTH} ${CHART_HEIGHT + 14}`}
        role="img"
        aria-label={`Prix par heure, de ${formatPrice(minPrice, priceMode, chargeKWh)} à ${formatPrice(maxPrice, priceMode, chargeKWh)}, actuellement ${formatPrice(priceAtHour(priced, nowHour), priceMode, chargeKWh)}`}
      >
        <line x1={0} y1={CHART_HEIGHT} x2={CHART_WIDTH} y2={CHART_HEIGHT} className="hourly-price-chart-baseline" />
        {bars.map((bar) => (
          <g key={bar.hour}>
            <rect
              x={bar.x}
              y={CHART_HEIGHT - bar.height}
              width={barWidth}
              height={bar.height}
              rx={2}
              className={`hourly-price-chart-bar${bar.isNow ? " hourly-price-chart-bar--now" : ""}`}
            />
            {bar.showLabel && (
              <text
                x={bar.x + barWidth / 2}
                y={CHART_HEIGHT - bar.height - 4}
                textAnchor="middle"
                className={`hourly-price-chart-value${bar.isNow ? " hourly-price-chart-value--now" : ""}`}
              >
                {(bar.price / 100).toFixed(2)}
              </text>
            )}
          </g>
        ))}
        {TICK_HOURS.map((h) => (
          <text
            key={h}
            x={(h / HOURS) * CHART_WIDTH + barWidth / 2}
            y={CHART_HEIGHT + 12}
            textAnchor="middle"
            className="hourly-price-chart-label"
          >
            {String(h).padStart(2, "0")}h
          </text>
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
