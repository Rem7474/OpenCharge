#!/usr/bin/env python3
import json
import time
from pathlib import Path
from typing import Any, Dict, Optional, Tuple, List, Set
from concurrent.futures import ThreadPoolExecutor, as_completed

import requests

BASE_URL = "https://fronts-map.izivia.com/api"

# Carré de carte (exemple: Annecy)
SQUARE = {
    "centerLng": 6.1,
    "centerLat": 45.9,
    "zoom": 9,
}

FILTERS: Dict[str, Any] = {}

# Limiter un peu la pression sur l’API
DETAIL_SLEEP = 0.0
PRICING_SLEEP = 0.0
MAX_WORKERS = 12  # nombre de threads en parallèle

JSONL_PATH = Path("izivia_all_data.jsonl")

# Headers côté front (pour charging-locations + pricing-info-items)
COMMON_HEADERS = {
    "Accept": "application/json",
    "Accept-Encoding": "gzip, deflate, br, zstd",
    "Accept-Language": "fr",
    "User-Agent": (
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "
        "AppleWebKit/537.36 (KHTML, like Gecko) "
        "Chrome/150.0.0.0 Safari/537.36 Edg/150.0.0.0"
    ),
    "Referer": "https://fronts-map.izivia.com/",
    "Origin": "https://fronts-map.izivia.com",
    "x-device-id": "b1a5a1c8-68b4-41fb-a18f-78d53910878a",
    "Content-Type": "application/json",
}

SESSION = requests.Session()
SESSION.headers.update(COMMON_HEADERS)


# ---------- Utils JSONL ----------

def load_existing_station_ids(path: Path) -> Set[str]:
    """Lit le JSONL existant et retourne le set des station.id déjà présents."""
    ids: Set[str] = set()
    if not path.exists():
        return ids

    print(f"Lecture de {path} pour détecter les stations déjà traitées…")

    with path.open("r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                record = json.loads(line)
            except json.JSONDecodeError:
                continue
            station = record.get("station") or {}
            sid = station.get("id")
            if sid:
                ids.add(sid)

    print(f"{len(ids)} station(s) déjà présentes dans le fichier.")
    return ids


def append_record(path: Path, record: Dict[str, Any]) -> None:
    """Écrit un record en JSONL (append)."""
    with path.open("a", encoding="utf-8") as f:
        f.write(json.dumps(record, ensure_ascii=False) + "\n")


# ---------- HTTP helpers ----------

def fetch_markers(square: Dict[str, Any], filters: Dict[str, Any]) -> List[Dict[str, Any]]:
    """
    Récupère les markers dans le carré.
    IMPORTANT: on utilise un simple requests.post sans SESSION ni headers spéciaux.
    """
    payload = {"square": square, "filters": filters}
    resp = requests.post(
        f"{BASE_URL}/map/markers",
        json=payload,
        timeout=10,
    )
    print("markers status:", resp.status_code, "content-type:", resp.headers.get("Content-Type"))
    resp.raise_for_status()

    try:
        data = resp.json()
    except json.JSONDecodeError as e:
        print("Body markers (300 chars):", resp.text[:300])
        raise RuntimeError(f"Erreur JSON sur /map/markers: {e}") from e

    if not isinstance(data, list):
        raise ValueError(f"markers payload inattendu: {type(data)}")
    return data


def fetch_station_details(station_id: str) -> Tuple[Optional[Dict[str, Any]], int, Optional[str]]:
    """Récupère les détails d’une station (via SESSION + headers front)."""
    try:
        resp = SESSION.post(
            f"{BASE_URL}/charging-locations/{station_id}",
            json={},
            timeout=10,
        )
    except requests.RequestException as e:
        return None, 0, f"RequestException: {e}"

    status = resp.status_code
    if status != 200:
        return None, status, resp.text

    try:
        return resp.json(), status, None
    except ValueError as e:
        return None, status, f"JSON decode error: {e}"


def fetch_pricing_items(station_id: str, station_emip_id: str) -> Tuple[Optional[Any], int, Optional[str]]:
    """
    Récupère les infos de pricing pour une station.
    Gère les cas: HTTP != 200, body vide, JSON invalide.
    """
    params = {"stationEmipId": station_emip_id}
    try:
        resp = SESSION.get(
            f"{BASE_URL}/charging-locations/{station_id}/pricing-info-items",
            params=params,
            timeout=10,
        )
    except requests.RequestException as e:
        return None, 0, f"RequestException: {e}"

    status = resp.status_code
    text = resp.text or ""

    if status != 200:
        return None, status, text

    if not text.strip():
        return None, status, "empty body"

    try:
        return resp.json(), status, None
    except ValueError as e:
        return None, status, f"JSON decode error: {e}"


# ---------- Traitement d’un marker ----------

def process_marker(marker: Dict[str, Any]) -> Dict[str, Any]:
    """
    Traite un marker: station + pricing + erreurs.
    Retourne un record complet prêt à être écrit.
    """
    station_id = marker.get("id")
    record: Dict[str, Any] = {
        "marker": marker,
        "station": None,
        "pricing": None,
        "errors": {},
    }

    if not station_id:
        record["errors"]["station"] = {
            "status": None,
            "message": "marker without id",
        }
        return record

    # Détail station
    station_data, detail_status, detail_error = fetch_station_details(station_id)
    time.sleep(DETAIL_SLEEP)

    if station_data is None:
        record["errors"]["station"] = {
            "status": detail_status,
            "message": detail_error,
        }
        return record

    record["station"] = station_data

    # Pricing
    station_emip_id = station_data.get("firstStationEmipId")
    if station_emip_id:
        pricing_data, pricing_status, pricing_error = fetch_pricing_items(
            station_id, station_emip_id
        )
        time.sleep(PRICING_SLEEP)

        if pricing_data is not None:
            record["pricing"] = pricing_data
            record["errors"]["pricing"] = {
                "status": pricing_status,
                "message": None,
            }
        else:
            record["errors"]["pricing"] = {
                "status": pricing_status,
                "message": pricing_error,
            }
    else:
        record["errors"]["pricing"] = {
            "status": None,
            "message": "no firstStationEmipId in station_data",
        }

    return record


# ---------- Main ----------

def main():
    # 1. Lire les stations déjà présentes
    existing_ids = load_existing_station_ids(JSONL_PATH)

    # 2. Récupérer les markers
    markers = fetch_markers(SQUARE, FILTERS)
    print(f"Markers récupérés: {len(markers)}")

    # 3. Filtrer ceux déjà connus
    markers_to_process = [
        m for m in markers
        if m.get("id") not in existing_ids
    ]
    print(f"Markers à traiter (non présents dans le JSONL): {len(markers_to_process)}")

    if not markers_to_process:
        print("Rien à faire, toutes les stations de cette zone sont déjà dans le JSONL.")
        return

    # S'assurer que le fichier existe
    JSONL_PATH.parent.mkdir(parents=True, exist_ok=True)
    if not JSONL_PATH.exists():
        JSONL_PATH.touch()

    # 4. Paralléliser le traitement
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
                record = future.result()
            except Exception as e:
                # En cas de bug, on log et on écrit un record minimal
                station_id = marker.get("id")
                print(f"[ERROR] station_id={station_id}: {e}")
                record = {
                    "marker": marker,
                    "station": None,
                    "pricing": None,
                    "errors": {
                        "station": {"status": None, "message": f"exception: {e}"},
                    },
                }

            append_record(JSONL_PATH, record)
            done += 1
            if done % 50 == 0 or done == total:
                print(f"{done}/{total} nouveaux records écrits")

    print(f"Traitement terminé, JSONL mis à jour: {JSONL_PATH}")


if __name__ == "__main__":
    main()