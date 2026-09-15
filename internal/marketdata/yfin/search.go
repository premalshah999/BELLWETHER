package yfin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// maxSearchResults bounds a response. A watchlist picker showing fifty
// near-identical tickers helps nobody.
const maxSearchResults = 25

type searchResponse struct {
	Results []struct {
		Symbol   string `json:"symbol"`
		Name     string `json:"name"`
		Exchange string `json:"exchange"`
		Type     string `json:"type"`
	} `json:"results"`
	Error string `json:"error"`
}

// SearchSymbols finds instruments by name or ticker.
//
// The sidecar returns symbols already in canonical form, so a result can be
// handed straight to the watchlist without further translation. A row whose
// symbol this application cannot parse is dropped rather than offered: showing
// someone a company they cannot then add is worse than not showing it.
func (c *Client) SearchSymbols(ctx context.Context, query string, limit int) ([]marketdata.SearchResult, error) {
	if !c.Configured() {
		return nil, fmt.Errorf("%w: no yfinance sidecar configured", marketdata.ErrNotSupported)
	}
	query = strings.TrimSpace(query)
	if len(query) < 2 {
		return nil, nil
	}
	if limit <= 0 || limit > maxSearchResults {
		limit = 10
	}

	params := url.Values{"q": {query}, "limit": {strconv.Itoa(limit)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/search?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("yfinance: build search request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("yfinance: search unreachable: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("yfinance: read search response: %w", err)
	}

	var parsed searchResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("yfinance: decode search response (http %d): %w", resp.StatusCode, err)
	}
	if parsed.Error != "" {
		return nil, fmt.Errorf("yfinance: search: %s", parsed.Error)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("yfinance: search returned http %d", resp.StatusCode)
	}

	out := make([]marketdata.SearchResult, 0, len(parsed.Results))
	for _, r := range parsed.Results {
		sym, err := marketdata.ParseSymbol(r.Symbol)
		if err != nil {
			continue
		}
		out = append(out, marketdata.SearchResult{
			Symbol:   sym,
			Name:     r.Name,
			Exchange: r.Exchange,
			Kind:     r.Type,
		})
	}
	return out, nil
}

var _ marketdata.SymbolSearcher = (*Client)(nil)
