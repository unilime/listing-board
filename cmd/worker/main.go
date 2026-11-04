package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"listingboard/internal/store"
	"listingboard/internal/worker"
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}
	pause := 2 * time.Second
	if raw := os.Getenv("STEP_PAUSE"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed < 0 {
			log.Fatalf("STEP_PAUSE %q is not a valid Go duration", raw)
		}
		pause = parsed
	}
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "data"
	}

	st, err := store.OpenRetry(dsn, 60*time.Second)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Printf("worker started, step pause %s", pause)
	(&worker.Worker{Store: st, DataDir: dataDir, Pause: pause}).Run(ctx)
}
