package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/config"
	"github.com/tradesys/dashboard/internal/news/company"
	"github.com/tradesys/dashboard/internal/smartmoney"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// newSmartMoneySyncer builds the insider and fund sync over the S&P 1500.
// Returns nil when SEC_USER_AGENT is unset: SEC refuses anonymous clients.
func newSmartMoneySyncer(cfg *config.Config, store *postgres.DB, log *slog.Logger) (*smartmoney.Syncer, error) {
	if strings.TrimSpace(cfg.SECUserAgent) == "" {
		return nil, nil
	}
	_, listings, err := company.LoadEmbeddedUS()
	if err != nil {
		return nil, err
	}
	universe := make(map[string]string, len(listings))
	for _, l := range listings {
		if cik := strings.TrimLeft(l.CIK, "0"); cik != "" {
			universe[cik] = l.Symbol
		}
	}
	client := &http.Client{Timeout: time.Minute}
	return &smartmoney.Syncer{
		Store:    store,
		SEC:      &smartmoney.SEC{HTTP: client, UserAgent: cfg.SECUserAgent},
		HTTP:     client,
		Log:      log,
		Universe: universe,
	}, nil
}

// runSyncSmartMoney is the -sync-smartmoney one-off: insider trades for the
// last N days, then every followed fund's latest two 13Fs.
func runSyncSmartMoney(days int) error {
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
	s, err := newSmartMoneySyncer(cfg, store, log)
	if err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("SEC_USER_AGENT is not set; SEC refuses requests without a contact")
	}
	n, err := s.SyncInsiders(ctx, days)
	if err != nil {
		return err
	}
	f, err := s.SyncFunds(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("\n  %d insider trades stored from the last %d days; %d fund filings added.\n\n", n, days, f)
	return nil
}
