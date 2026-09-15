package news

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestParseFederalRegisterLive hits the real, live, free, keyless Federal
// Register API and confirms the actual (unexported) parser this engine uses
// produces well-formed items with the agency fact embedded the way
// interpret() (in internal/events) expects to read it back.
func TestParseFederalRegisterLive(t *testing.T) {
	if os.Getenv("SKIP_LIVE_TESTS") != "" {
		t.Skip("SKIP_LIVE_TESTS set")
	}
	src := Source{
		ID: "federal-register", Method: MethodFederalRegister,
		URL: "https://www.federalregister.gov/api/v1/documents.json?per_page=5&order=newest" +
			"&conditions%5Btype%5D%5B%5D=RULE&conditions%5Btype%5D%5B%5D=PRORULE&conditions%5Btype%5D%5B%5D=NOTICE",
		Usage: UsageOfficial, Trust: TrustOfficial,
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(src.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	e := &Engine{}
	items, err := e.parseFederalRegister(src, body, time.Now().UTC())
	if err != nil {
		t.Fatalf("parseFederalRegister: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("no items parsed from a live response")
	}
	var sawAgency bool
	for _, it := range items {
		if it.Title == "" || it.URL == "" {
			t.Errorf("incomplete item: %+v", it)
		}
		if it.SourceID != src.ID {
			t.Errorf("SourceID = %q, want %q", it.SourceID, src.ID)
		}
		if strings.Contains(it.Description, "|AGENCY:") {
			sawAgency = true
		}
	}
	if !sawAgency {
		t.Error("no item carried an AGENCY fact -- every live Federal Register document has an issuing agency")
	}
}
