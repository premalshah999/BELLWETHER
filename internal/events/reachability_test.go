package events

import (
	"os"
	"sort"
	"strings"
	"testing"
)

// archiveOnlyTypes are event types nothing can produce any more.
//
// They were reachable through an exchange's filing-subject vocabulary, which went with
// that exchange's parser. The constants stay because events classified under
// them are still in the archive and a constant is how those rows are read
// back; removing one would not tidy anything, it would make old data
// unreadable.
//
// Pinned so the two directions of drift are both visible. A type that leaves
// this list has gained a producer, which is usually the point of adding one. A
// type that joins it has lost its last producer, which is almost never
// deliberate -- that is what happened to nineteen of these at once, silently,
// and nothing said so.
var archiveOnlyTypes = []string{
	"TypeAnnualReport",
	"TypeBoardMeeting",
	"TypeConcall",
	"TypeDemise",
	"TypeEGM",
	"TypeFundNAV",
	"TypeInvestorPresentation",
	"TypeNewPlant",
	"TypeNewsVerify",
	"TypeNewspaperPublication",
	"TypePartnership",
	"TypePriceMovement",
	"TypePromoterTransaction",
	"TypeRecordDate",
	"TypeRegulatoryApproval",
	"TypeRightsIssue",
	"TypeStockSplit",
	"TypeTradingWindow",
	"TypeVolumeSpurt",
}

// TestEveryTypeIsEitherProducibleOrKnownToBeArchiveOnly keeps the taxonomy
// honest about what it can still assign.
//
// The producers are the keyword rules, the SEC filing interpreter and the
// sector fan-out. A type named by none of them cannot be assigned to anything
// arriving today, however many rows already carry it.
func TestEveryTypeIsEitherProducibleOrKnownToBeArchiveOnly(t *testing.T) {
	producers := readAll(t, "rules.go", "sec_filing.go", "sector.go")
	declared := declaredTypes(t)

	known := map[string]bool{}
	for _, n := range archiveOnlyTypes {
		known[n] = true
	}

	var newlyOrphaned, revived []string
	for _, name := range declared {
		producible := strings.Contains(producers, name)
		switch {
		case !producible && !known[name]:
			newlyOrphaned = append(newlyOrphaned, name)
		case producible && known[name]:
			revived = append(revived, name)
		}
	}

	if len(newlyOrphaned) > 0 {
		sort.Strings(newlyOrphaned)
		t.Errorf("these types lost their last producer and nothing can assign them now: %s\n"+
			"If that was deliberate, add them to archiveOnlyTypes. If it was not, a rule "+
			"was removed that something still depends on.", strings.Join(newlyOrphaned, ", "))
	}
	if len(revived) > 0 {
		sort.Strings(revived)
		t.Errorf("these types gained a producer and are no longer archive-only: %s\n"+
			"Remove them from archiveOnlyTypes.", strings.Join(revived, ", "))
	}
}

func readAll(t *testing.T, files ...string) string {
	t.Helper()
	var b strings.Builder
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		b.Write(src)
	}
	return b.String()
}

func declaredTypes(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("taxonomy.go")
	if err != nil {
		t.Fatalf("read taxonomy.go: %v", err)
	}
	var names []string
	for _, line := range strings.Split(string(src), "\n") {
		if i := strings.Index(line, " Type = \""); i > 0 {
			names = append(names, strings.TrimSpace(line[:i]))
		}
	}
	if len(names) < 40 {
		t.Fatalf("found only %d type constants; the parser is wrong", len(names))
	}
	return names
}
