# Frontend minimal

Frontend statique Leaflet pour tester l'API du backend Go.

## Lancer

1. Démarrer le backend sur `http://localhost:8080`.
2. Servir ce dossier, par exemple:

```powershell
cd c:\Code\test\frontend
python -m http.server 5173
```

3. Ouvrir `http://localhost:5173`.

Le frontend interroge:

- `GET /operators`
- `GET /stations?limit=500&offset=...`
