package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"opencharge/internal/api"
	"opencharge/internal/logging"
	"opencharge/internal/repository"
)

func main() {
	logger := logging.New()
	slog.SetDefault(logger)

	dsn := getEnv("DATABASE_URL", "postgres://opencharge:opencharge@localhost:5432/opencharge?sslmode=disable")
	addr := getEnv("LISTEN_ADDR", ":8080")
	corsOrigin := getEnv("CORS_ORIGIN", "*")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := repository.Open(ctx, dsn)
	if err != nil {
		logger.Error("connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	stationRepo := repository.NewStationRepository(pool)
	tariffRepo := repository.NewTariffRepository(pool)
	stationsHandler := api.NewStationsHandler(stationRepo, tariffRepo)
	freshmileHandler := api.NewFreshmileHandler()

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(logging.RequestLogger(logger))
	router.Use(middleware.Recoverer)
	router.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{corsOrigin},
		AllowedMethods: []string{http.MethodGet, http.MethodOptions},
		AllowedHeaders: []string{"*"},
		MaxAge:         300,
	}))

	router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	router.Get("/stations", stationsHandler.ListStations)
	router.Get("/stations/{id}", stationsHandler.GetStation)
	router.Get("/sources", stationsHandler.ListSources)
	router.Get("/freshmile/availability/{locationId}", freshmileHandler.GetAvailability)

	server := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("opencharge-api listening", "addr", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("http server", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	logger.Info("opencharge-api shutting down")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "error", err)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
