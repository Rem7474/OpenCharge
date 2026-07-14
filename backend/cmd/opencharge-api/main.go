package main

import (
	"context"
	"log"
	"os"

	"github.com/Rem7474/opencharge/internal/api"
	"github.com/Rem7474/opencharge/internal/repository"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	dbURL := os.Getenv("DB_URL")
	if dbURL == "" {
		dbURL = "postgres://opencharge:opencharge@localhost:5432/opencharge?sslmode=disable"
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("Cannot connect to DB: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("DB ping failed: %v", err)
	}
	log.Println("[DB] Connected to PostgreSQL+PostGIS")

	// Repositories
	stationRepo := repository.NewStationRepository(pool)
	tariffRepo := repository.NewTariffRepository(pool)
	linkRepo := repository.NewLinkRepository(pool)

	// Handlers
	h := api.NewStationHandler(stationRepo, tariffRepo, linkRepo)

	// Router
	r := gin.Default()

	// CORS: autorise le frontend local et les envs de prod
	r.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"http://localhost:3000", "http://localhost:5173", "https://opencharge.remcorp.fr"},
		AllowMethods:     []string{"GET", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept"},
		AllowCredentials: false,
	}))

	v1 := r.Group("/api/v1")
	{
		v1.GET("/stations", h.ListStations)
		v1.GET("/stations/:id", h.GetStation)
	}

	// Health check
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	log.Printf("[API] Démarrage sur le port %s", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
