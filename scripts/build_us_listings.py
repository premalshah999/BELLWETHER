#!/usr/bin/env python3
"""Regenerate internal/news/company/data/us_listings.csv.

The US scan universe. It defines what the scanner covers, what the
fundamentals runner refreshes, what peer groups compare against, and -- most
visibly -- which events reach the default market feed, because that feed
admits an event only when one of its companies is a constituent.

It was the S&P 500 alone, 504 names, against 750 for NSE. On a US-first
product that is backwards, and it showed: of 1,850 US events in a 72-hour
window only 532 had a company inside the universe, so 71% of correctly
ingested, correctly resolved US news never reached the feed.

This builds the S&P 1500 (500 + 400 MidCap + 600 SmallCap) instead, which is
the same kind of thing the NSE list is -- a liquid, recognizable subset of a
much larger listed universe -- at a comparable size.

Sectors come from Wikipedia's constituent tables, which carry GICS sector and
sub-industry. CIK and exchange come from us_tickers.csv, which is SEC's own
company_tickers_exchange.json: the filer's declared identity beats a
third-party transcription of it, and the CIK is the join key for every SEC
filing the app ingests.

Usage:  python3 scripts/build_us_listings.py [--dry-run]
"""

import argparse
import csv
import pathlib
import sys

import pandas as pd

UA = "TradeSys/1.0 (you@example.com)"
ROOT = pathlib.Path(__file__).resolve().parent.parent
DATA = ROOT / "internal" / "news" / "company" / "data"
TICKERS = DATA / "us_tickers.csv"
OUT = DATA / "us_listings.csv"

# Names kept in the universe that no S&P index carries.
#
# INFY is the live US/NSE ticker collision: Infosys trades as INFY.NSE in
# Mumbai and as an ADR under the bare ticker INFY on NYSE. Two instruments,
# two currencies, one string -- which is the case the whole venue-qualified
# symbol scheme exists to keep apart, and it is only a real test of that
# scheme while both sides are actually in the data.
#
# ABB was the other documented collision and is deliberately not here: ABB
# Ltd's US ADR has left NYSE, SEC's exchange file no longer carries it, and
# only ABB.NSE remains. A collision that no longer exists should not be
# preserved by hand.
EXTRAS = [
    {"symbol": "INFY", "name": "Infosys Ltd", "sector": "Information Technology",
     "sub_industry": "IT Consulting & Other Services", "cik": "0001067491",
     "exchange": "NYSE"},
]

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

    for extra in EXTRAS:
        if extra["symbol"] in seen:
            continue
        if extra["symbol"] not in sec:
            print(f"warning: extra {extra['symbol']} is no longer in SEC's "
                  f"exchange file; it may have delisted", file=sys.stderr)
        seen.add(extra["symbol"])
        rows.append(dict(extra))
    print(f"extras: {len(EXTRAS)} kept outside the indexes")

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
