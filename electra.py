#!/usr/bin/env python3
import json
from pathlib import Path

import requests

URL = "https://stations.go-electra.com/stations.js"
OUTPUT_PATH = Path("electra_raw.jsonl")


def fetch_electra_stations():
    resp = requests.get(URL, timeout=15)
    resp.raise_for_status()
    text = resp.text

    # stations.js commence par `export default [` et finit par `];`
    # On enlève le préfixe et le suffixe pour obtenir du JSON valide.
    prefix = "export default"
    if text.startswith(prefix):
        text = text[len(prefix):].lstrip()

    # Enlever un éventuel point-virgule final
    text = text.strip()
    if text.endswith(";"):
        text = text[:-1].strip()

    # À ce stade, text doit être un tableau JSON de stations
    stations = json.loads(text)
    if not isinstance(stations, list):
        raise ValueError(f"Payload inattendu: {type(stations)}")

    return stations


def main():
    stations = fetch_electra_stations()
    print(f"Stations Electra récupérées: {len(stations)}")

    OUTPUT_PATH.parent.mkdir(parents=True, exist_ok=True)
    with OUTPUT_PATH.open("w", encoding="utf-8") as f:
        for st in stations:
            # On peut déjà ajouter la source pour la normalisation future
            record = {
                "source": "electra",
                "raw": st,
            }
            f.write(json.dumps(record, ensure_ascii=False) + "\n")

    print(f"Fichier JSONL écrit: {OUTPUT_PATH}")


if __name__ == "__main__":
    main()