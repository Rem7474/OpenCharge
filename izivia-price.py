#!/usr/bin/env python3
import json
from pathlib import Path
from typing import Any, Dict, Optional, Tuple

import requests

BASE_URL = "https://fronts-map.izivia.com/api"

INPUT_PATH = Path("izivia_all_data.jsonl")
OUTPUT_PATH = Path("izivia_all_data_pricing_fixed.jsonl")

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
    "Content-Type": "application/json",
}

SESSION = requests.Session()
SESSION.headers.update(COMMON_HEADERS)


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
        # 200 mais body vide → pas de pricing exploitable
        return None, status, "empty body"

    try:
        return resp.json(), status, None
    except ValueError as e:
        return None, status, f"JSON decode error: {e}"


def needs_pricing_fix(record: Dict[str, Any]) -> bool:
    """
    Retourne True si ce record mérite une tentative de recalcul de pricing.
    Cas typiques :
      - pricing est None
      - pricing existe mais errors.pricing est renseigné
    """
    if record.get("pricing") is not None:
        # on considère que pricing est déjà renseigné
        # si tu veux forcer une mise à jour, tu peux changer cette logique
        return False

    errors = record.get("errors") or {}
    pricing_err = errors.get("pricing")

    # Si on a déjà un statut 200 + empty body, tu peux décider de ne pas retenter.
    if pricing_err:
        status = pricing_err.get("status")
        message = pricing_err.get("message") or ""
        # exemple: HTTP 500, JSON error, etc. → OK pour retenter
        # pour empty body/200 tu peux choisir de ne pas retenter (ici on retente)
        return True

    # pas de pricing du tout, pas d'erreur → on retente
    return True


def main():
    if not INPUT_PATH.exists():
        print(f"Fichier d'entrée introuvable: {INPUT_PATH}")
        return

    print(f"Lecture de {INPUT_PATH}…")
    OUTPUT_PATH.parent.mkdir(parents=True, exist_ok=True)

    fixed_count = 0
    total_count = 0

    with INPUT_PATH.open("r", encoding="utf-8") as fin, OUTPUT_PATH.open(
        "w", encoding="utf-8"
    ) as fout:

        for line in fin:
            total_count += 1
            line = line.strip()
            if not line:
                continue

            try:
                record = json.loads(line)
            except json.JSONDecodeError as e:
                print(f"[ligne {total_count}] JSON invalide, copie brute: {e}")
                fout.write(line + "\n")
                continue

            station = record.get("station") or {}
            station_id = station.get("id")
            station_emip_id = station.get("firstStationEmipId")

            # Si pas de station ou pas de firstStationEmipId → impossible de fixer pricing
            if not station_id or not station_emip_id:
                fout.write(json.dumps(record, ensure_ascii=False) + "\n")
                continue

            if needs_pricing_fix(record):
                print(f"[fix {total_count}] station_id={station_id} emip_id={station_emip_id}")
                pricing_data, pricing_status, pricing_error = fetch_pricing_items(
                    station_id, station_emip_id
                )

                if pricing_data is not None:
                    record["pricing"] = pricing_data
                    # On peut vider l'erreur ou la mettre à jour
                    errors = record.get("errors") or {}
                    errors["pricing"] = {
                        "status": pricing_status,
                        "message": None,
                    }
                    record["errors"] = errors
                    fixed_count += 1
                else:
                    # On met à jour l'erreur pricing
                    errors = record.get("errors") or {}
                    errors["pricing"] = {
                        "status": pricing_status,
                        "message": pricing_error,
                    }
                    record["errors"] = errors

            # Écrit le record (fixé ou original) dans le nouveau fichier
            fout.write(json.dumps(record, ensure_ascii=False) + "\n")

    print(f"Total records lus: {total_count}")
    print(f"Records avec pricing fixé: {fixed_count}")
    print(f"Fichier corrigé écrit: {OUTPUT_PATH}")


if __name__ == "__main__":
    main()