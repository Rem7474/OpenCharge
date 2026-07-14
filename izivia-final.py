#!/usr/bin/env python3
import json
import re
from pathlib import Path
from typing import Any, Dict, List, Optional
from concurrent.futures import ThreadPoolExecutor, as_completed

import requests

BASE_URL = "https://fronts-map.izivia.com/api"

# Carré de carte à interroger (exemple: autour d'Annecy)
SQUARE = {
    "centerLng": 6.1,
    "centerLat": 45.9,
    "zoom": 9,
}

FILTERS: Dict[str, Any] = {}

MAX_WORKERS = 12

# Fichier de sortie: stations Izivia déjà normalisées
NORMALIZED_PATH = Path("izivia_normalized.jsonl")

# Headers front pour charging-locations + pricing-info-items
COMMON_HEADERS = {
    "Accept": "application/json",
    "Accept-Language": "fr",
    "User-Agent": (
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "
        "AppleWebKit/537.36 (KHTML, like Gecko) "
        "Chrome/150.0.0.0 Safari/537.36 Edg/150.0.0.0"
    ),
    "Referer": "https://fronts-map.izivia.com/",
    "Origin": "https://fronts-map.izivia.com",
    "x-device-id": "b1a5a1c8-68b4-41fb-a18f-78d53910878a",
}

SESSION = requests.Session()
SESSION.headers.update(COMMON_HEADERS)
SESSION.headers.pop("Accept-Encoding", None)  # laisser requests gérer la décompression


# ---------- Utils JSONL ----------

def load_existing_station_ids(path: Path) -> set[str]:
    """Lit le JSONL normalisé et retourne les station.id déjà présents."""
    ids: set[str] = set()
    if not path.exists():
        return ids

    print(f"Lecture de {path} pour détecter les stations déjà normalisées…")

    with path.open("r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                rec = json.loads(line)
            except json.JSONDecodeError:
                continue
            sid = rec.get("id")
            if sid:
                ids.add(sid)

    print(f"{len(ids)} station(s) Izivia déjà normalisées.")
    return ids


def append_normalized(record: Dict[str, Any]) -> None:
    """Écrit une station normalisée en JSONL (append)."""
    NORMALIZED_PATH.parent.mkdir(parents=True, exist_ok=True)
    with NORMALIZED_PATH.open("a", encoding="utf-8") as f:
        f.write(json.dumps(record, ensure_ascii=False) + "\n")


# ---------- HTTP helpers ----------

def fetch_markers(square: Dict[str, Any], filters: Dict[str, Any]) -> List[Dict[str, Any]]:
    """
    Récupère les markers dans le carré.
    Important: on utilise un POST simple sans headers spéciaux.
    """
    payload = {"square": square, "filters": filters}
    resp = requests.post(
        f"{BASE_URL}/map/markers",
        json=payload,
        timeout=10,
    )
    print("markers status:", resp.status_code, "CT:", resp.headers.get("Content-Type"))
    resp.raise_for_status()

    try:
        data = resp.json()
    except json.JSONDecodeError as e:
        print("Body markers (300 chars):", resp.text[:300])
        raise RuntimeError(f"Erreur JSON sur /map/markers: {e}") from e

    if not isinstance(data, list):
        raise ValueError(f"markers payload inattendu: {type(data)}")
    return data


def fetch_station_details(station_id: str) -> Optional[Dict[str, Any]]:
    """Récupère les détails d’une station."""
    try:
        resp = SESSION.post(
            f"{BASE_URL}/charging-locations/{station_id}",
            json={},
            timeout=10,
        )
    except requests.RequestException as e:
        print(f"[station {station_id}] RequestException details: {e}")
        return None

    if resp.status_code != 200:
        print(f"[station {station_id}] HTTP {resp.status_code}: {resp.text[:200]}")
        return None

    try:
        return resp.json()
    except json.JSONDecodeError as e:
        print(f"[station {station_id}] JSON error: {e}, body={resp.text[:200]}")
        return None


def fetch_pricing_items(station_id: str, station_emip_id: str) -> Optional[Any]:
    """Récupère les infos de pricing pour une station."""
    params = {"stationEmipId": station_emip_id}
    try:
        resp = SESSION.get(
            f"{BASE_URL}/charging-locations/{station_id}/pricing-info-items",
            params=params,
            timeout=10,
        )
    except requests.RequestException as e:
        print(f"[pricing {station_id}] RequestException: {e}")
        return None

    text = resp.text or ""
    ct = resp.headers.get("Content-Type")
    status = resp.status_code

    if status != 200:
        print(f"[pricing {station_id}] HTTP {status}, CT={ct}, body={text[:200]}")
        return None

    if not text.strip():
        print(f"[pricing {station_id}] HTTP 200, CT={ct}, empty body")
        return None

    try:
        return resp.json()
    except json.JSONDecodeError as e:
        print(f"[pricing {station_id}] JSON error: {e}, CT={ct}, body={text[:200]}")
        return None


# ---------- Normalisation ----------

def map_standard_to_kind(standard: str) -> str:
    s = standard.lower()
    if "combo" in s:
        return "dc"
    return "ac"


def normalize_connectors(station: Dict[str, Any]) -> List[Dict[str, Any]]:
    stats = station.get("chargingConnectorsStats") or []
    connectors: List[Dict[str, Any]] = []

    for c in stats:
        standard = c.get("standard")
        kind = map_standard_to_kind(standard or "")

        max_power_w = c.get("maxPowerInW")
        max_power_kw = float(max_power_w) / 1000.0 if max_power_w is not None else None

        total_count = c.get("totalConnectorCount")
        available_count = c.get("availableConnectorCount")

        connectors.append(
            {
                "kind": kind,
                "standards": [standard] if standard else [],
                "maxPowerKw": max_power_kw,
                "count": total_count,
                "availableCount": available_count,
            }
        )

    return connectors


def parse_pricing_info_text(text: str) -> Dict[str, Any]:
    price_per_kwh = None
    service_fee_percent = None
    currency = "EUR"

    m_price = re.search(r"([0-9]+,[0-9]+)\s*€", text)
    if m_price:
        price_str = m_price.group(1).replace(",", ".")
        try:
            price_per_kwh = float(price_str)
        except ValueError:
            pass

    m_fee = re.search(r"([0-9]+)%\s+de frais de service", text)
    if m_fee:
        try:
            service_fee_percent = float(m_fee.group(1))
        except ValueError:
            pass

    return {
        "price_per_kwh": price_per_kwh,
        "currency": currency,
        "service_fee_percent": service_fee_percent,
        "raw": text.strip(),
    }


def normalize_pricing(pricing: Any) -> Dict[str, Any]:
    if not pricing:
        return {}

    tiers: List[Dict[str, Any]] = []

    try:
        for item in pricing:
            charging_stations = item.get("chargingStations") or []
            for cs in charging_stations:
                names = cs.get("chargingStationNames") or []
                texts = cs.get("pricingInfos") or cs.get("rawPricingInfos") or []
                if not texts:
                    continue
                info = parse_pricing_info_text(texts[0])

                tier = {
                    "names": names,
                    **info,
                    "itemType": cs.get("itemType"),
                    "structureType": cs.get("structureType"),
                }
                tiers.append(tier)
    except Exception as e:
        print(f"[normalize_pricing] exception: {e}")
        return {"model": "izivia_text", "tiers": []}

    return {
        "model": "izivia_text",
        "tiers": tiers,
    }


def normalize_station(station: Dict[str, Any], pricing: Any) -> Dict[str, Any]:
    sid = station.get("id")
    name = station.get("name")
    address = station.get("address") or {}
    coords = station.get("coordinates") or []

    street = address.get("street")
    postal_code = address.get("postalCode")
    city = address.get("city")
    country = address.get("country")

    lng = coords[0] if len(coords) >= 1 else None
    lat = coords[1] if len(coords) >= 2 else None

    status = station.get("status")
    opening_hours = station.get("openingHours") or {}
    hours = opening_hours.get("hours") or {}
    is24_7 = bool(hours.get("twentyFourSeven"))

    parking_type = station.get("parkingType")
    accessible = station.get("accessibleForDisabled")

    connectors = normalize_connectors(station)
    pricing_norm = normalize_pricing(pricing)

    return {
        "id": f"izivia:{sid}",
        "source": "izivia",
        "operator": "Izivia",
        "name": name,
        "status": status,
        "address": {
            "street": street,
            "postalCode": postal_code,
            "city": city,
            "countryCode": country,
        },
        "location": {
            "lat": lat,
            "lng": lng,
        },
        "parkingType": parking_type,
        "accessibleForDisabled": accessible,
        "is24_7": is24_7,
        "connectors": connectors,
        "pricing": pricing_norm,
    }


# ---------- Traitement d’un marker ----------

def process_marker(marker: Dict[str, Any]) -> Optional[Dict[str, Any]]:
    station_id = marker.get("id")
    if not station_id:
        return None

    station = fetch_station_details(station_id)
    if not station:
        return None

    first_emip = station.get("firstStationEmipId")
    pricing_data = None
    if first_emip:
        pricing_data = fetch_pricing_items(station_id, first_emip)

    normalized = normalize_station(station, pricing_data)
    return normalized


# ---------- Main ----------

def main():
    existing_ids = load_existing_station_ids(NORMALIZED_PATH)

    markers = fetch_markers(SQUARE, FILTERS)
    print(f"Markers récupérés: {len(markers)}")

    markers_to_process = [
        m for m in markers
        if f"izivia:{m.get('id')}" not in existing_ids
    ]
    print(f"Markers à traiter (non présents dans le normalisé): {len(markers_to_process)}")

    if not markers_to_process:
        print("Rien à faire pour cette zone.")
        return

    NORMALIZED_PATH.parent.mkdir(parents=True, exist_ok=True)
    if not NORMALIZED_PATH.exists():
        NORMALIZED_PATH.touch()

    total = len(markers_to_process)
    done = 0

    with ThreadPoolExecutor(max_workers=MAX_WORKERS) as executor:
        futures = {
            executor.submit(process_marker, marker): marker
            for marker in markers_to_process
        }

        for future in as_completed(futures):
            marker = futures[future]
            try:
                norm = future.result()
            except Exception as e:
                sid = marker.get("id")
                print(f"[ERROR] station_id={sid}: {e}")
                norm = None

            if norm is not None:
                append_normalized(norm)
                done += 1
                if done % 50 == 0 or done == total:
                    print(f"{done}/{total} stations normalisées écrites")

    print(f"Terminé. Fichier normalisé: {NORMALIZED_PATH}")


if __name__ == "__main__":
    main()