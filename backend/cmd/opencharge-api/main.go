package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/Rem7474/opencharge/internal/api"
	"github.com/Rem7474/opencharge/internal/repository"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

func main() {
	logger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("init logger: %v", err)
	}
	defer logger.Sync()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://opencharge:opencharge@localhost:5432/opencharge?sslmode=disable"
	}

	// Run migrations
	m, err := migrate.New("file://db/migrations", dbURL)
	if err != nil {
		logger.Fatal("migration init", zap.Error(err))
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		logger.Fatal("migration up", zap.Error(err))
	}
	logger.Info("Migrations applied")

	// Connect pool
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		logger.Fatal("pgxpool connect", zap.Error(err))
	}
	defer pool.Close()

	stationRepo := repository.NewStationRepository(pool)
	tariffRepo := repository.NewTariffRepository(pool)
	linkRepo := repository.NewLinkRepository(pool)

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(ginZapLogger(logger))
	r.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"http://localhost:5173", "http://localhost:3000"},
		AllowMethods:     []string{"GET", "OPTIONS"},
		AllowHeaders:     []string{"Content-Type", "Authorization"},
		AllowCredentials: false,
	}))

	v1 := r.Group("/api/v1")
	api.NewStationsHandler(stationRepo, tariffRepo, linkRepo, logger).RegisterRoutes(v1)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	logger.Info("Starting OpenCharge API", zap.String("port", port))
	if err := r.Run(fmt.Sprintf(":%s", port)); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}

func ginZapLogger(logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		logger.Info("request",
			zap.String("method", c.Request.Method),
			zap.String("path", c.Request.URL.Path),
			zap.Int("status", c.Writer.Status()),
		)
	}
}
