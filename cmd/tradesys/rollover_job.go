package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/tradesys/dashboard/internal/config"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// runRolloverNews moves aged news to the archive now rather than waiting for
// the nightly job.
//
// The reason this exists is the first run. An existing database has months of
// news to move in batches to an endpoint on the other side of the internet,
// and that should be something an operator starts deliberately and watches,
// not something that surprises them at 02:30. Afterwards the scheduled run
// moves a day's worth and finishes in seconds.
func runRolloverNews(reclaim bool) error {
	ctx := context.Background()
	cfg, err := config.Load(".env")
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	if cfg.NewsArchiveURL == "" {
		return fmt.Errorf("rollover: NEWS_ARCHIVE_DATABASE_URL is not configured")
	}

	store, err := openStore(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer store.Close()

	opt, err := venueMigrationOption()
	if err != nil {
		return err
	}
	archive, err := postgres.OpenArchive(ctx, store, cfg.NewsArchiveURL, cfg.NewsHotWindow,
		postgres.WithLogger(log), opt)
	if err != nil {
		return err
	}
	defer archive.Close()

	moved, err := archive.Rollover(ctx, time.Now())
	if err != nil {
		// Partial progress is kept, so saying how far it got is useful.
		return fmt.Errorf("rollover: moved %d events before failing: %w", moved, err)
	}
	log.Info("news rollover complete", "events", moved)

	// Reclaiming is opt-in because it locks each table while it rewrites it.
	// Without it the rollover stops the primary growing rather than shrinking
	// it -- see ReclaimSpace for why that distinction matters on a plan with a
	// storage ceiling.
	if reclaim {
		log.Warn("reclaiming space: each news table is locked while it is rewritten, " +
			"so ingestion stalls and the feed errors until this finishes")
		if err := archive.ReclaimSpace(ctx); err != nil {
			return fmt.Errorf("rollover: reclaim space: %w", err)
		}
	}
	return nil
}
