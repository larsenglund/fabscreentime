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
	flag.Parse()

	store, err := server.OpenStore(*dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer store.Close()

	srv := &http.Server{
		Addr:              *addr,
		Handler:           server.New(store).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
