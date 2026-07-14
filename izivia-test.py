import requests

BASE_URL = "https://fronts-map.izivia.com/api"

COMMON_HEADERS = {
    "Accept": "application/json",
    # NE PAS définir Accept-Encoding, requests mettra ce qu’il gère
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

session = requests.Session()
session.headers.update(COMMON_HEADERS)

station_id = "6a4169bf4f691657885b3b5b"       # à adapter
station_emip_id = "FR*HPC*ENF080072*004*3"          # à adapter

resp = session.get(
    f"{BASE_URL}/charging-locations/{station_id}/pricing-info-items",
    params={"stationEmipId": station_emip_id},
    timeout=10,
)

print("status:", resp.status_code)
print("content-type:", resp.headers.get("Content-Type"))
print("raw body (300 chars):")
print(resp.text[:300])

try:
    data = resp.json()
    print("JSON OK, sample:")
    print(data[:1])
except Exception as e:
    print("JSON decode error:", e)