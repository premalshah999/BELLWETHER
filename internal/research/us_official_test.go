package research

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

// These hit the real, live SEC and Federal Register APIs -- both free and
// keyless, so there is no secret to gate the test on -- and are skipped in
// CI-style environments that block outbound network access.
func liveClient(t *testing.T) *http.Client {
	t.Helper()
	if os.Getenv("SKIP_LIVE_TESTS") != "" {
		t.Skip("SKIP_LIVE_TESTS set")
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func TestSECFullTextScraperLive(t *testing.T) {
	s := &SECFullTextScraper{
		Client: liveClient(t), UserAgent: "TradeSys/1.0 (test@example.com)", Forms: "8-K",
	}
	if !s.Configured() {
		t.Fatal("Configured() = false with a UserAgent set")
	}
	findings, err := s.Search(context.Background(), `"material agreement"`, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("no findings from a query that returns thousands of hits")
	}
	for _, f := range findings {
		if f.Title == "" || f.URL == "" {
			t.Errorf("incomplete finding: %+v", f)
		}
		if !hasPrefix(f.URL, "https://www.sec.gov/Archives/edgar/data/") {
			t.Errorf("URL = %q, want an SEC Archives URL", f.URL)
		}
		if f.Trust != 100 {
			t.Errorf("Trust = %d, want 100 (TrustOfficial)", f.Trust)
		}
	}
}

func TestSECFullTextScraperUnconfigured(t *testing.T) {
	s := &SECFullTextScraper{}
	if s.Configured() {
		t.Fatal("Configured() = true with no UserAgent")
	}
	if _, err := s.Search(context.Background(), "x", 5); err == nil {
		t.Fatal("Search should refuse to run without a declared contact -- SEC 403s an undeclared one")
	}
}

func TestFederalRegisterScraperLive(t *testing.T) {
	f := &FederalRegisterScraper{Client: liveClient(t)}
	findings, err := f.Search(context.Background(), "tariff", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("no findings for a query that reliably returns thousands of documents")
	}
	for _, r := range findings {
		if r.Title == "" || r.URL == "" {
			t.Errorf("incomplete finding: %+v", r)
		}
		if !hasPrefix(r.URL, "https://www.federalregister.gov/documents/") {
			t.Errorf("URL = %q, want a federalregister.gov document URL", r.URL)
		}
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
