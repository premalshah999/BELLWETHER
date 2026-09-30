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
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news/company"
	"github.com/tradesys/dashboard/internal/smartmoney"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// newSmartMoneySyncer builds the insider and fund sync over every company
// with a US listing, not only the scan universe: an insider buying a small
// cap is as much news as one buying a giant. Returns nil when SEC_USER_AGENT
// is unset: SEC refuses anonymous clients.
func newSmartMoneySyncer(cfg *config.Config, store *postgres.DB, log *slog.Logger) (*smartmoney.Syncer, error) {
	if strings.TrimSpace(cfg.SECUserAgent) == "" {
		return nil, nil
	}
	tickers, _, err := company.LoadEmbeddedUS()
	if err != nil {
		return nil, err
	}
	universe := make(map[string]string, len(tickers))
	for _, t := range tickers {
		// SEC lists an issuer's primary share class first.
		if cik := strings.TrimLeft(t.CIK, "0"); cik != "" && universe[cik] == "" {
			universe[cik] = t.Symbol
		}
	}
	client := &http.Client{Timeout: time.Minute}
	return &smartmoney.Syncer{
		Store:    store,
		SEC:      &smartmoney.SEC{HTTP: client, UserAgent: cfg.SECUserAgent},
		HTTP:     client,
		Log:      log,
		Universe: universe,
		FIGIKey:  cfg.OpenFIGIKey,
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

// insiderSymbolOf maps a ticker as an issuer reported it to a listed symbol.
func insiderSymbolOf(master *company.Master) func(string) (string, bool) {
	return func(ticker string) (string, bool) {
		sym, err := marketdata.ParseSymbol(ticker)
		if err != nil {
			return "", false
		}
		c, ok := master.Lookup(sym.String())
		return c.Symbol, ok
	}
}

// runBackfillInsiders loads the newest `quarters` of SEC's insider datasets.
func runBackfillInsiders(quarters int) error {
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
	master, err := company.Load()
	if err != nil {
		return err
	}
	n, err := s.BackfillInsiders(ctx, quarters, insiderSymbolOf(master))
	fmt.Printf("\n  %d insider trades stored from %d quarterly datasets.\n\n", n, quarters)
	return err
}
