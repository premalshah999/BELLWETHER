#!/usr/bin/env python3
"""Regenerate internal/news/company/data/us_listings.csv.

The US scan universe. It defines what the scanner covers, what the
fundamentals runner refreshes, what peer groups compare against, and -- most
visibly -- which events reach the default market feed, because that feed
admits an event only when one of its companies is a constituent.

It is the S&P 1500 (500 + 400 MidCap + 600 SmallCap): a liquid, recognizable
subset of a much larger listed universe. The S&P 500 alone was too narrow: of
1,850 events in a 72-hour window only 532 had a company inside it, so 71% of
correctly ingested, correctly resolved news never reached the feed.

Sectors come from Wikipedia's constituent tables, which carry GICS sector and
sub-industry. CIK and exchange come from us_tickers.csv, which is SEC's own
company_tickers_exchange.json: the filer's declared identity beats a
third-party transcription of it, and the CIK is the join key for every SEC
filing the app ingests.

Usage:  SEC_USER_AGENT="App/1.0 (you@example.com)" \
        python3 scripts/build_us_listings.py [--dry-run]
"""

import argparse
import csv
import os
import pathlib
import sys

import pandas as pd

# Wikipedia, like SEC, asks automated clients to name a contact. Same
# variable the app uses: "AppName/1.0 (you@example.com)".
UA = os.environ.get("SEC_USER_AGENT", "").strip()
ROOT = pathlib.Path(__file__).resolve().parent.parent
DATA = ROOT / "internal" / "news" / "company" / "data"
TICKERS = DATA / "us_tickers.csv"
OUT = DATA / "us_listings.csv"

INDEXES = [
    ("S&P 500", "https://en.wikipedia.org/wiki/List_of_S%26P_500_companies"),
    ("S&P 400", "https://en.wikipedia.org/wiki/List_of_S%26P_400_companies"),
    ("S&P 600", "https://en.wikipedia.org/wiki/List_of_S%26P_600_companies"),
]


def normalize(ticker: str) -> str:
    """Wikipedia writes class shares as BRK.B; SEC writes BRK-B.

    The dot is this app's venue separator, so it can never appear inside a
    ticker -- see marketdata.ParseSymbol. SEC's spelling is the correct one.
    """
    return ticker.strip().upper().replace(".", "-")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--dry-run", action="store_true", help="report, write nothing")
    args = ap.parse_args()
    if not UA:
        sys.exit("set SEC_USER_AGENT, e.g. \"Bellwether/1.0 (you@example.com)\"")

    if not TICKERS.exists():
        print(f"missing {TICKERS}", file=sys.stderr)
        return 1

    # SEC reference: symbol -> (cik, exchange).
    sec = {}
    with TICKERS.open(newline="", encoding="utf-8") as f:
        for row in csv.DictReader(f):
            sec[normalize(row["symbol"])] = (row["cik"], row["exchange"])
    print(f"SEC reference: {len(sec)} registrants")

    rows, seen, unmatched = [], set(), []
    for name, url in INDEXES:
        table = pd.read_html(url, attrs={"id": "constituents"},
                             storage_options={"User-Agent": UA})[0]
        added = 0
        for _, r in table.iterrows():
            sym = normalize(str(r["Symbol"]))
            if not sym or sym in seen:
                continue
            hit = sec.get(sym)
            if hit is None:
                # Usually a ticker changed between Wikipedia and SEC's file.
                # Dropped rather than guessed: a wrong CIK silently
                # misattributes every filing that company makes.
                unmatched.append((name, sym))
                continue
            cik, exchange = hit
            seen.add(sym)
            rows.append({
                "symbol": sym,
                "name": str(r["Security"]).strip(),
                "sector": str(r["GICS Sector"]).strip(),
                "sub_industry": str(r["GICS Sub-Industry"]).strip(),
                "cik": cik,
                "exchange": exchange,
            })
            added += 1
        print(f"{name}: {len(table)} listed, {added} added")

    rows.sort(key=lambda r: r["symbol"])
    print(f"\ntotal: {len(rows)} constituents")
    if unmatched:
        print(f"unmatched against SEC ({len(unmatched)}): "
              + ", ".join(f"{s}" for _, s in unmatched[:20]))

    by_sector = {}
    for r in rows:
        by_sector[r["sector"]] = by_sector.get(r["sector"], 0) + 1
    print("sectors: " + ", ".join(f"{k} {v}" for k, v in sorted(by_sector.items())))

    if args.dry_run:
        print("\n--dry-run: nothing written")
        return 0

    with OUT.open("w", newline="", encoding="utf-8") as f:
        w = csv.DictWriter(f, fieldnames=["symbol", "name", "sector",
                                          "sub_industry", "cik", "exchange"])
        w.writeheader()
        w.writerows(rows)
    print(f"\nwrote {OUT} ({len(rows)} rows)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
