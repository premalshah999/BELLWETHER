package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/tradesys/dashboard/internal/config"

	"github.com/tradesys/dashboard/internal/events"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// mergeWindow is how far apart two copies of one story may be discovered.
const mergeWindow = 36 * time.Hour

// runMergeDuplicates cleans aggregator suffixes off recent headlines and
// folds events that are the same story into one.
//
// Two copies are the same story when their cleaned headlines have the same
// token set, they were found within a day and a half of each other, and they
// do not name two different companies. The earliest copy is kept, because
// its discovery time is the moment the story was first knowable.
func runMergeDuplicates(ctx context.Context, db *postgres.DB, days int, log *slog.Logger) error {
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	list, err := db.RecentEventsForDedupe(ctx, since)
	if err != nil {
		return err
	}
	type kept struct {
		id      int64
		at      time.Time
		symbols map[string]bool
	}
	byKey := map[string][]*kept{}
	merges := map[int64][]int64{}
	retitled := 0

	for _, e := range list {
		head := events.StripPublisher(e.Headline, e.Publisher)
		key := events.TitleKey(head)
		if head != e.Headline || key != e.TitleKey {
			if err := db.RetitleEvent(ctx, e.ID, head, key); err != nil {
				return fmt.Errorf("retitle %d: %w", e.ID, err)
			}
			retitled++
		}
		if key == "" {
			continue
		}
		syms := map[string]bool{}
		for _, s := range e.Symbols {
			syms[s] = true
		}
		placed := false
		for _, k := range byKey[key] {
			if e.DiscoveredAt.Sub(k.at) > mergeWindow {
				continue
			}
			compatible := len(syms) == 0 || len(k.symbols) == 0
			for s := range syms {
				if k.symbols[s] {
					compatible = true
					break
				}
			}
			if !compatible {
				continue
			}
			merges[k.id] = append(merges[k.id], e.ID)
			for s := range syms {
				k.symbols[s] = true
			}
			placed = true
			break
		}
		if !placed {
			byKey[key] = append(byKey[key], &kept{id: e.ID, at: e.DiscoveredAt, symbols: syms})
		}
	}

	merged := 0
	for keeper, dups := range merges {
		if err := db.MergeEvents(ctx, keeper, dups, time.Now()); err != nil {
			return err
		}
		merged += len(dups)
	}
	log.Info("duplicate merge finished", "events", len(list), "retitled", retitled,
		"duplicates_merged", merged, "stories_affected", len(merges))
	fmt.Printf("\n  %d events checked since %s.\n  %d headlines cleaned, %d duplicates folded into %d stories.\n\n",
		len(list), since.Format("Jan 2"), retitled, merged, len(merges))
	return nil
}

func runMergeDuplicatesCmd(days int) error {
	ctx := context.Background()
	cfg, err := config.Load(".env")
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	store, err := openStore(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer store.Close()
	return runMergeDuplicates(ctx, store, days, log)
}
