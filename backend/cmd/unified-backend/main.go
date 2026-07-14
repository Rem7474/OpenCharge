package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"chargingbackend/internal/importer"
	"chargingbackend/internal/model"
	"chargingbackend/internal/store"
)

func main() {
	var (
		workspace = flag.String("workspace", ".", "workspace contenant les fichiers d'origine")
		dbPath    = flag.String("db", "charging.db", "chemin de la base SQLite")
		serveAddr = flag.String("addr", ":8080", "adresse d'écoute HTTP")
		ingest    = flag.Bool("ingest", true, "ingérer les fichiers du workspace dans la base")
		serve     = flag.Bool("serve", true, "démarrer l'API HTTP")
		quiet     = flag.Bool("quiet", false, "désactiver les logs d'information")
	)
	flag.Parse()
	if *quiet {
		log.SetOutput(io.Discard)
	}

	ctx := context.Background()
	st, err := store.Open(*dbPath)
	if err != nil {
		fatalf("open store: %v", err)
	}
	defer st.Close()

	if *ingest {
		result, err := importer.LoadWorkspace(*workspace)
		if err != nil {
			fatalf("ingest workspace: %v", err)
		}
		for _, station := range result.Stations {
			if err := st.UpsertStation(ctx, station); err != nil {
				fatalf("upsert %s: %v", station.ID, err)
			}
		}
		count, err := st.CountStations(ctx)
		if err == nil && !*quiet {
			log.Printf("ingested=%d stations=%d details=%s", len(result.Stations), count, result.Summary())
		}
	}

	if !*serve {
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": time.Now().UTC()})
	})
	mux.HandleFunc("/stations", func(w http.ResponseWriter, r *http.Request) {
		filter := model.StationFilter{
			Source:   r.URL.Query().Get("source"),
			Operator: r.URL.Query().Get("operator"),
			City:     r.URL.Query().Get("city"),
			Sort:     r.URL.Query().Get("sort"),
		}
		if value := r.URL.Query().Get("limit"); value != "" {
			if parsed, err := strconv.Atoi(value); err == nil {
				filter.Limit = parsed
			}
		}
		if value := r.URL.Query().Get("offset"); value != "" {
			if parsed, err := strconv.Atoi(value); err == nil {
				filter.Offset = parsed
			}
		}
		if value := r.URL.Query().Get("min_price"); value != "" {
			if parsed, err := strconv.ParseFloat(strings.ReplaceAll(value, ",", "."), 64); err == nil {
				filter.MinPrice = &parsed
			}
		}

		stations, err := st.ListStations(r.Context(), filter)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"count": len(stations), "stations": stations})
	})
	mux.HandleFunc("/stations/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/stations/")
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		station, err := st.GetStation(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, station)
	})
	mux.HandleFunc("/operators", func(w http.ResponseWriter, r *http.Request) {
		stations, err := st.ListStations(r.Context(), model.StationFilter{Limit: 500, Sort: "name"})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		seen := map[string]struct{}{}
		operators := make([]string, 0)
		for _, station := range stations {
			if _, ok := seen[station.Operator]; ok {
				continue
			}
			seen[station.Operator] = struct{}{}
			operators = append(operators, station.Operator)
		}
		writeJSON(w, http.StatusOK, map[string]any{"operators": operators})
	})

	server := &http.Server{
		Addr:              *serveAddr,
		Handler:           withCORS(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	if !*quiet {
		log.Printf("API listening on %s", *serveAddr)
	}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fatalf("serve: %v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("write json: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}
