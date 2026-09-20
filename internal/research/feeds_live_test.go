package research

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/news"
)

// Opt-in because upstream availability is not a deterministic CI dependency.
func TestAdditionalOfficialFeedsLive(t *testing.T) {
	if os.Getenv("BELLWETHER_LIVE_SOURCES") != "1" {
		t.Skip("set BELLWETHER_LIVE_SOURCES=1 to verify public feeds")
	}
	for _, src := range news.AdditionalOfficialSources() {
		t.Run(src.ID, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			reader := &FeedScraper{Source: src, Client: &http.Client{Timeout: 15 * time.Second}}
			_, err := reader.Search(ctx, "inflation energy interest competition economy", 12)
			if err != nil {
				t.Fatal(err)
			}
			if len(reader.items) == 0 {
				t.Fatal("feed returned no documents")
			}
			t.Logf("%d documents parsed", len(reader.items))
		})
	}
}
