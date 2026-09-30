package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/tradesys/dashboard/internal/algo"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// First-run seeding, so a fresh deployment is not an empty screen.

// seedWatchlist populates an empty watchlist on first run.
func seedWatchlist(ctx context.Context, store *postgres.DB, log *slog.Logger) error {
	existing, err := store.ListWatchlist(ctx)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	for _, raw := range seedSymbols {
		sym, err := marketdata.ParseSymbol(raw)
		if err != nil {
			return fmt.Errorf("seed symbol %q: %w", raw, err)
		}
		if err := store.AddWatchlist(ctx, sym, ""); err != nil {
			return err
		}
	}
	log.Info("seeded an empty watchlist", "symbols", seedSymbols)
	return nil
}

// seedAlgorithms installs the prebuilt templates on first run.
//
// They arrive disabled. A fresh installation must not start sending
// notifications nobody has looked at yet, but the builder should have real
// examples to open rather than a blank editor.
func seedAlgorithms(ctx context.Context, store *postgres.DB, log *slog.Logger) error {
	existing, err := store.ListAlgorithms(ctx)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	var names []string
	for _, tpl := range algo.Templates() {
		a := *tpl.Algorithm
		if _, err := store.CreateAlgorithm(ctx, &a); err != nil {
			return fmt.Errorf("seed template %q: %w", tpl.Key, err)
		}
		names = append(names, a.Name)
	}
	log.Info("seeded algorithm templates (all disabled)", "algorithms", names)
	return nil
}
