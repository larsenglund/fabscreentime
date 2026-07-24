// Command fabscreentimed is the FabScreenTime backend: one static binary that
// serves the JSON API, the dashboard, and (in later phases) the signed agent
// artifacts, backed by a single SQLite file (PLAN.md §6, §8).
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/larsenglund/fabscreentime/internal/server"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	dbPath := flag.String("db", "fabscreentime.db", "path to the SQLite database")
	agentDir := flag.String("agentdir", "", "directory holding the signed agent release (manifest.json + agent.exe)")
	rollupEvery := flag.Duration("rollup", time.Hour, "how often to recompute dirty daily rollups")
	flag.Parse()

	store, err := server.OpenStore(*dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app := server.New(store, *agentDir)
	// Recompute recent days first: the dirty set is in-memory, so a restart
	// would otherwise drop rollups for days ingested just before shutdown.
	app.CatchUpRollups(3)
	app.StartRollupLoop(ctx, *rollupEvery)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("fabscreentimed listening on %s (db=%s)", *addr, *dbPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("shutting down…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
