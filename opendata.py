#!/usr/bin/env python3
import csv
import json
from pathlib import Path

import requests

URL = "https://www.data.gouv.fr/api/1/datasets/r/2729b192-40ab-4454-904d-735084dca3a3"

RAW_PATH = Path("irve_raw.dat")      # fichier brut
JSONL_PATH = Path("irve_raw.jsonl")  # optionnel si CSV


def download_resource():
    resp = requests.get(URL, timeout=30)
    resp.raise_for_status()

    content_type = resp.headers.get("Content-Type", "")
    print("Content-Type:", content_type)

    RAW_PATH.parent.mkdir(parents=True, exist_ok=True)
    RAW_PATH.write_bytes(resp.content)
    print(f"Fichier brut enregistré dans: {RAW_PATH}")

    return content_type


def csv_to_jsonl(csv_path: Path, jsonl_path: Path, encoding="utf-8"):
    print(f"Conversion CSV -> JSONL: {csv_path} -> {jsonl_path}")
    jsonl_path.parent.mkdir(parents=True, exist_ok=True)

    with csv_path.open("r", encoding=encoding, newline="") as fin, \
            jsonl_path.open("w", encoding="utf-8") as fout:
        reader = csv.DictReader(fin, delimiter=";")  # souvent ';' sur data.gouv, à adapter si besoin
        for row in reader:
            record = {
                "source": "data.gouv.irve",
                "raw": row,
            }
            fout.write(json.dumps(record, ensure_ascii=False) + "\n")

    print("Conversion terminée.")


def main():
    content_type = download_resource()

    # si c'est un CSV, on le convertit en JSONL
    if "text/csv" in content_type or RAW_PATH.suffix.lower() == ".csv":
        # renommer le fichier brut en .csv pour plus de clarté
        csv_path = RAW_PATH.with_suffix(".csv")
        RAW_PATH.rename(csv_path)
        csv_to_jsonl(csv_path, JSONL_PATH)
    else:
        print("Le fichier n'est pas un CSV, je laisse le brut tel quel.")
        # si c'est du JSON, tu pourras le parser directement plus tard


if __name__ == "__main__":
    main()