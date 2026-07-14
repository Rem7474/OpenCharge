package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"chargingbackend/internal/scraper"
)

func main() {
	var (
		workspace = flag.String("workspace", ".", "workspace racine qui recevra les fichiers générés")
		outDir    = flag.String("out", "", "dossier de sortie; vide = workspace")
		workers   = flag.Int("workers", 12, "nombre de workers pour Izivia")
		gridStep  = flag.Float64("grid-step", 2.0, "pas de la grille Izivia en degrés")
		zoom      = flag.Int("zoom", 7, "zoom map pour Izivia")
		timeout   = flag.Duration("timeout", 0, "timeout global du scraping")
		quiet     = flag.Bool("quiet", false, "désactiver les logs d'information")
	)
	flag.Parse()

	ctx := context.Background()
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}

	summary, err := scraper.ScrapeAll(ctx, scraper.Config{
		Workspace: *workspace,
		OutDir:    *outDir,
		Workers:   *workers,
		GridStep:  *gridStep,
		Zoom:      *zoom,
	})
	if err != nil {
		fatalf("scrape all: %v", err)
	}

	if !*quiet {
		log.Printf("scrape terminé: electra=%d izivia=%d irve=%d total=%d", summary.Electra, summary.Izivia, summary.IRVE, summary.Total)
		log.Printf("fichiers générés dans %s", func() string {
			if *outDir != "" {
				return *outDir
			}
			return *workspace
		}())
		log.Printf("date=%s", time.Now().UTC().Format(time.RFC3339))
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
