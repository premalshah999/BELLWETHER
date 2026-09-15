"""
yfinance sidecar.

The Go application talks to market data providers over HTTP behind one
interface. This service is one more of those providers: it wraps the yfinance
library and speaks the same normalised JSON as the rest.

It exists for three reasons the hosted APIs do not cover on a free tier:

  * depth   — five or more years of daily history, not 100 bars
  * breadth — NSE and BSE listings, which Twelve Data gates behind a paid plan
  * cost    — no daily request budget to ration

It binds to loopback inside the compose network and is never exposed publicly.
"""

from __future__ import annotations

import json
import logging
import math
import os
import threading
import time
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

import pandas as pd
import yfinance as yf

log = logging.getLogger("yfin")

# Intervals the Go side may ask for, mapped to yfinance's spelling.
INTERVALS = {
    "1m": "1m", "5m": "5m", "15m": "15m",
    "1h": "1h", "1d": "1d", "1wk": "1wk",
}

# Yahoo caps intraday history hard. Asking beyond these returns an empty frame
# rather than an error, so the caps are applied here instead.
MAX_PERIOD = {
    "1m": "7d", "5m": "59d", "15m": "59d",
    "1h": "729d", "1d": "max", "1wk": "max",
}

# A short in-process cache. The Go router caches too, but a burst of requests
# for the same symbol during a page load should cost one upstream fetch.
CACHE_TTL = 60.0
_cache: dict[tuple, tuple[float, dict]] = {}
_cache_lock = threading.Lock()


# The furthest back each intraday interval may be asked for, in calendar days.
INTRADAY_CAP_DAYS = {"1m": 7, "5m": 59, "15m": 59, "1h": 729}


def _period_for(interval: str, years: float) -> str:
    """Pick the smallest period that covers the requested span."""
    cap = MAX_PERIOD[interval]
    if interval in ("1d", "1wk"):
        if years <= 1:
            return "1y"
        if years <= 2:
            return "2y"
        if years <= 5:
            return "5y"
        if years <= 10:
            return "10y"
        return "max"

    # Intraday used to ignore the caller entirely and return the cap, so a
    # thirty-bar chart pulled fifty-nine days of five-minute data -- roughly
    # 4,400 bars fetched to draw thirty. The span is honoured now, with two
    # days of slack so the first bar of the window is never the first bar
    # available, and still clamped to what Yahoo will serve.
    days = math.ceil(years * 365) + 2
    return f"{max(2, min(INTRADAY_CAP_DAYS[interval], days))}d"


def fetch(symbol: str, interval: str, years: float, adjust: bool) -> dict:
    """Fetch candles and corporate actions for one symbol."""
    period = _period_for(interval, years)

    ticker = yf.Ticker(symbol)
    frame = ticker.history(
        period=period,
        interval=INTERVALS[interval],
        # Split adjustment is not optional: without it a chart spanning a
        # split shows a cliff that never happened. Dividend adjustment is the
        # caller's choice, because it changes what the price *means* — total
        # return rather than what the stock actually traded at.
        auto_adjust=adjust,
        actions=True,
        raise_errors=False,
    )

    if frame is None or frame.empty:
        return {"symbol": symbol, "interval": interval, "candles": [], "adjusted": adjust}

    frame = frame.dropna(subset=["Open", "High", "Low", "Close"])

    candles = []
    for ts, row in frame.iterrows():
        if isinstance(ts, pd.Timestamp):
            moment = ts.tz_convert("UTC") if ts.tzinfo else ts.tz_localize("UTC")
        else:
            continue
        volume = row.get("Volume", 0)
        candles.append({
            "t": moment.isoformat().replace("+00:00", "Z"),
            "o": float(row["Open"]),
            "h": float(row["High"]),
            "l": float(row["Low"]),
            "c": float(row["Close"]),
            "v": float(0 if pd.isna(volume) else volume),
        })

    # Corporate actions travel with the data so the Go side can label a chart
    # honestly rather than leaving an unexplained gap.
    splits = []
    try:
        # A pandas Series raises on truth-testing, so check length explicitly
        # rather than relying on `or {}`.
        series = ticker.splits
        if series is not None and len(series) > 0:
            for ts, ratio in series.items():
                splits.append({"date": ts.date().isoformat(), "ratio": float(ratio)})
    except Exception as exc:
        log.debug("no split data for %s: %s", symbol, exc)

    return {
        "symbol": symbol,
        "interval": interval,
        "candles": candles,
        "adjusted": adjust,
        "splits": splits[-12:],
        "currency": (frame.attrs.get("currency") if hasattr(frame, "attrs") else None) or "",
    }


def cached_fetch(symbol: str, interval: str, years: float, adjust: bool) -> dict:
    key = (symbol, interval, years, adjust)
    now = time.time()
    with _cache_lock:
        hit = _cache.get(key)
        if hit and now - hit[0] < CACHE_TTL:
            return hit[1]

    payload = fetch(symbol, interval, years, adjust)

    with _cache_lock:
        _cache[key] = (now, payload)
        # Bound the cache; this process is long-lived.
        if len(_cache) > 512:
            oldest = sorted(_cache.items(), key=lambda kv: kv[1][0])[:128]
            for k, _ in oldest:
                _cache.pop(k, None)
    return payload


# Yahoo's exchange codes, mapped to the canonical suffix the app uses.
EXCHANGE_SUFFIX = {"NSI": "NSE", "BSE": "BSE"}

# Instrument types worth offering. Yahoo's search mixes in mutual funds,
# currencies and index codes, which are noise in a stock watchlist.
SEARCHABLE_TYPES = {"EQUITY", "ETF"}


def search(query: str, limit: int) -> list[dict]:
    """Look up instruments by name or ticker, normalised to canonical symbols."""
    result = yf.Search(query, max_results=min(limit * 3, 30))
    out: list[dict] = []
    seen: set[str] = set()

    for q in result.quotes or []:
        raw = (q.get("symbol") or "").strip()
        kind = (q.get("quoteType") or "").upper()
        if not raw or kind not in SEARCHABLE_TYPES:
            continue

        # Yahoo spells Indian listings RELIANCE.NS / RELINFRA.BO; the app
        # spells them RELIANCE.NSE / RELINFRA.BSE.
        exchange = (q.get("exchange") or "").upper()
        if "." in raw:
            base, _, suffix = raw.rpartition(".")
            canonical_suffix = {"NS": "NSE", "BO": "BSE"}.get(suffix.upper())
            if canonical_suffix is None:
                # A venue the app has no mapping for; skip rather than
                # offering something that cannot be charted.
                continue
            symbol = f"{base}.{canonical_suffix}"
        else:
            # Yahoo carries many non-US venues without a suffix. Only offer
            # the ones the app can actually resolve.
            if exchange not in ("NYQ", "NMS", "NGM", "NCM", "ASE", "PCX", "BTS"):
                continue
            symbol = raw

        if symbol in seen:
            continue
        seen.add(symbol)

        out.append({
            "symbol": symbol,
            "name": q.get("longname") or q.get("shortname") or symbol,
            "exchange": EXCHANGE_SUFFIX.get(exchange, exchange),
            "type": kind,
        })
        if len(out) >= limit:
            break
    return out


# --------------------------------------------------------------------------
# Market scan
#
# The point of scanning here rather than in Go is that the data already
# arrives as a pandas frame. Computing a rolling mean over 2,500 symbols is
# one vectorised operation in this process and several thousand round trips
# if the bars are shipped across first.
#
# What comes back is deliberately small: a handful of numbers per symbol
# rather than the bars they were derived from. The bars are already cached
# for the instruments anyone actually looks at, and shipping five years of
# OHLCV for 2,500 symbols to compute six statistics would be absurd.
# --------------------------------------------------------------------------

# Batch size for the bulk download.
#
# yfinance issues one request per chunk, so larger is fewer round trips — but
# a failure anywhere in a chunk costs the whole chunk. Fifty is small enough
# that one bad symbol is cheap to lose and large enough that 2,500 symbols is
# fifty requests rather than 2,500.
SCAN_CHUNK = 50

# Lookback for the statistics. Sixty trading days is a quarter: long enough
# for a 20-day mean to mean something, short enough that a stock which
# changed character three months ago is not judged against what it used to be.
# A full year, because the metrics derived from this window are named for one.
# At six months the high and low columns were labelled "52w" and computed over
# half that, which understates how far a stock is from its real extremes and
# is the kind of error a reader has no way to see.
SCAN_PERIOD = "1y"



# ---------------------------------------------------------------------------
# Fundamentals
#
# Two different kinds of number live here and they must not be conflated.
#
# The ratios from `info` are a snapshot of what the provider believes *right
# now*: a trailing P/E moves every time the price moves. They are stamped with
# the time we fetched them and nothing else, because there is no other honest
# timestamp for them.
#
# The statement line items are periodic and carry two dates. period_end is the
# quarter or year the numbers describe; report_date is when they were first
# published. Those are six to eight weeks apart for an Indian company, and
# using the former as if it were the latter is how a backtest comes to "know"
# June's results in June. Only report_date is knowledge time.

# The statement rows worth promoting to their own fields. Everything else is
# preserved verbatim, so a metric nobody asked for today can still be
# recovered later without refetching.
INCOME_FIELDS = {
    "Total Revenue": "revenue",
    "Gross Profit": "gross_profit",
    "Operating Income": "operating_income",
    "EBITDA": "ebitda",
    "EBIT": "ebit",
    "Net Income": "net_income",
    "Basic EPS": "eps_basic",
    "Diluted EPS": "eps_diluted",
    "Interest Expense": "interest_expense",
    "Tax Provision": "tax_provision",
    "Total Expenses": "total_expenses",
}

BALANCE_FIELDS = {
    "Total Assets": "total_assets",
    "Total Debt": "total_debt",
    "Net Debt": "net_debt",
    "Stockholders Equity": "equity",
    "Cash And Cash Equivalents": "cash",
    "Working Capital": "working_capital",
    "Invested Capital": "invested_capital",
    "Ordinary Shares Number": "shares_outstanding",
    "Tangible Book Value": "tangible_book_value",
}

CASHFLOW_FIELDS = {
    "Free Cash Flow": "free_cash_flow",
    "Capital Expenditure": "capex",
    "Operating Cash Flow": "operating_cash_flow",
}

INFO_FIELDS = [
    "trailingPE", "forwardPE", "priceToBook", "marketCap", "enterpriseValue",
    "returnOnEquity", "returnOnAssets", "debtToEquity", "currentRatio",
    "quickRatio", "trailingEps", "forwardEps", "bookValue", "dividendYield",
    "payoutRatio", "profitMargins", "operatingMargins", "grossMargins",
    "ebitdaMargins", "revenueGrowth", "earningsGrowth", "totalRevenue",
    "ebitda", "totalDebt", "totalCash", "freeCashflow", "operatingCashflow",
    "sharesOutstanding", "floatShares", "beta", "sector", "industry",
    "enterpriseToEbitda", "enterpriseToRevenue", "pegRatio",
    # The two currencies, which are not always the same one. Infosys reports
    # its statements in US dollars while its shares trade in rupees, so any
    # ratio built from both is in mixed units unless this is checked.
    "currency", "financialCurrency",
]


def _num(v):
    """A finite float, or None. Never NaN — it does not survive JSON."""
    try:
        f = float(v)
    except (TypeError, ValueError):
        return None
    if f != f or f in (float("inf"), float("-inf")):
        return None
    return f


def _report_dates(ticker):
    """Map each period end to the date its results were announced.

    yfinance dates statements by the period they cover. An Indian company
    reports six to eight weeks after quarter end, so treating the period end as
    the moment the numbers existed would let anything reading this table see
    results before they were public.

    earnings_dates carries the announcements. Each period end is matched to the
    first announcement on or after it; a period with no matching announcement
    gets None, and None must be read as "we do not know when this became
    public" rather than as any particular date.
    """
    try:
        ed = ticker.earnings_dates
    except Exception:
        return []
    if ed is None or len(ed.index) == 0:
        return []
    out = []
    for ts in ed.index:
        try:
            out.append(pd.Timestamp(ts).tz_convert("UTC").to_pydatetime())
        except Exception:
            try:
                out.append(pd.Timestamp(ts).tz_localize("UTC").to_pydatetime())
            except Exception:
                continue
    return sorted(out)


def _match_report_date(period_end, announcements):
    if not announcements or period_end is None:
        return None
    for a in announcements:
        if a.date() >= period_end.date():
            # A results announcement more than six months after the period it
            # covers is not that period's announcement; it is the next one and
            # the real one is missing.
            if (a.date() - period_end.date()).days <= 190:
                return a
            return None
    return None


def _statement(df, mapping, period_type, announcements):
    """Turn one statement frame into per-period records."""
    out = []
    if df is None or df.empty:
        return out
    for col in df.columns:
        try:
            period_end = pd.Timestamp(col).to_pydatetime()
        except Exception:
            continue
        rec = {
            "period_end": period_end.strftime("%Y-%m-%d"),
            "period_type": period_type,
            "report_date": None,
            "metrics": {},
            "raw": {},
        }
        rd = _match_report_date(period_end, announcements)
        if rd is not None:
            rec["report_date"] = rd.strftime("%Y-%m-%dT%H:%M:%SZ")
        series = df[col]
        for row_name, value in series.items():
            n = _num(value)
            if n is None:
                continue
            key = str(row_name)
            rec["raw"][key] = n
            if key in mapping:
                rec["metrics"][mapping[key]] = n
        if rec["raw"]:
            out.append(rec)
    return out


def fundamentals(symbol: str) -> dict:
    t = yf.Ticker(symbol)

    info = {}
    try:
        raw_info = t.info or {}
        for k in INFO_FIELDS:
            v = raw_info.get(k)
            if v is None:
                continue
            info[k] = v if isinstance(v, str) else _num(v)
        info = {k: v for k, v in info.items() if v is not None}
    except Exception as exc:
        logging.warning("info failed for %s: %s", symbol, exc)

    announcements = _report_dates(t)

    periods = []
    for getter, mapping, ptype in (
        (lambda: t.quarterly_income_stmt, INCOME_FIELDS, "quarterly"),
        (lambda: t.income_stmt, INCOME_FIELDS, "annual"),
        (lambda: t.quarterly_balance_sheet, BALANCE_FIELDS, "quarterly"),
        (lambda: t.balance_sheet, BALANCE_FIELDS, "annual"),
        (lambda: t.quarterly_cashflow, CASHFLOW_FIELDS, "quarterly"),
        (lambda: t.cashflow, CASHFLOW_FIELDS, "annual"),
    ):
        try:
            periods.extend(_statement(getter(), mapping, ptype, announcements))
        except Exception as exc:
            logging.debug("statement failed for %s: %s", symbol, exc)

    # Merge the three statements for the same period into one record. They are
    # fetched separately but describe the same quarter, and a reader asking for
    # "Q1 FY27" wants revenue and debt in one place.
    merged = {}
    for rec in periods:
        key = (rec["period_end"], rec["period_type"])
        cur = merged.setdefault(key, {
            "period_end": rec["period_end"],
            "period_type": rec["period_type"],
            "report_date": rec["report_date"],
            "metrics": {},
            "raw": {},
        })
        if cur["report_date"] is None:
            cur["report_date"] = rec["report_date"]
        cur["metrics"].update(rec["metrics"])
        cur["raw"].update(rec["raw"])

    ordered = sorted(merged.values(), key=lambda r: (r["period_type"], r["period_end"]), reverse=True)
    return {
        "symbol": symbol,
        "as_of": datetime.now(timezone.utc).isoformat(),
        "info": info,
        "periods": ordered,
    }


def _scan_metrics(frame) -> dict | None:
    """Reduce one symbol's bars to the numbers a scanner reasons about."""
    import math

    import numpy as np

    if frame is None or frame.empty or len(frame) < 25:
        return None

    close = frame["Close"].dropna()
    volume = frame["Volume"].dropna()
    if len(close) < 25 or len(volume) < 25:
        return None

    last = float(close.iloc[-1])
    prev = float(close.iloc[-2])
    if not math.isfinite(last) or not math.isfinite(prev) or prev <= 0:
        return None

    ret_1d = (last / prev - 1.0) * 100.0

    # Volume relative to its own recent normal.
    #
    # Measured in log space, which is not a refinement but a correctness
    # requirement. Trading volume is strongly right-skewed -- measured across
    # this universe, skewness runs from 0.9 to 6.4, where a normal
    # distribution is 0. A z-score on raw volume therefore reports the shape
    # of the stock's own distribution rather than the unusualness of today:
    # one stock read 41.8 sigma raw and 5.6 sigma in log space, which is not a
    # number that can be compared against anything.
    #
    # The centre is the median and the spread is the MAD, so that a spike
    # three weeks ago does not inflate the baseline and mask a spike today --
    # exactly the case where the scanner most needs to fire.
    recent_vol = volume.iloc[-61:-1]
    recent_vol = recent_vol[recent_vol > 0]
    today_vol = float(volume.iloc[-1])
    vol_z = 0.0
    vol_ratio = 0.0
    if len(recent_vol) >= 20 and today_vol > 0:
        logs = np.log(recent_vol.astype(float))
        med = float(logs.median())
        mad = float((logs - med).abs().median()) * 1.4826
        if mad > 1e-9 and math.isfinite(mad):
            vol_z = (math.log(today_vol) - med) / mad
        vol_median = float(recent_vol.median())
        if vol_median > 0:
            # Reported against the median rather than the mean for the same
            # reason: the mean of a skewed series sits above its typical day.
            vol_ratio = today_vol / vol_median

    # The same treatment for the price move: a 4% day is unremarkable for a
    # smallcap and extraordinary for a large bank.
    #
    # Returns stay in linear space -- they are roughly symmetric and can be
    # negative -- but the spread is still a MAD, because one gap day in the
    # lookback would otherwise raise the bar for every day after it.
    daily_returns = close.pct_change().dropna().iloc[-60:] * 100.0
    ret_z = 0.0
    if len(daily_returns) >= 20:
        r_med = float(daily_returns.median())
        r_mad = float((daily_returns - r_med).abs().median()) * 1.4826
        if r_mad > 1e-9 and math.isfinite(r_mad):
            ret_z = (ret_1d - r_med) / r_mad

    if not math.isfinite(vol_z):
        vol_z = 0.0
    if not math.isfinite(ret_z):
        ret_z = 0.0

    high_52w = float(close.max())
    low_52w = float(close.min())
    pct_from_high = ((last / high_52w) - 1.0) * 100.0 if high_52w > 0 else 0.0
    pct_from_low = ((last / low_52w) - 1.0) * 100.0 if low_52w > 0 else 0.0

    # Gap: today's open against yesterday's close. An overnight repricing is
    # a different event from an intraday drift of the same size.
    gap = 0.0
    if "Open" in frame:
        opens = frame["Open"].dropna()
        if len(opens) and math.isfinite(float(opens.iloc[-1])):
            gap = (float(opens.iloc[-1]) / prev - 1.0) * 100.0

    def clean(x: float) -> float:
        return round(float(x), 4) if math.isfinite(x) else 0.0

    return {
        "close": clean(last),
        "return_1d": clean(ret_1d),
        "return_5d": clean((last / float(close.iloc[-6]) - 1.0) * 100.0) if len(close) > 6 else 0.0,
        "return_z": clean(ret_z),
        "volume": clean(today_vol),
        "volume_ratio": clean(vol_ratio),
        "volume_z": clean(vol_z),
        "gap_percent": clean(gap),
        "pct_from_52w_high": clean(pct_from_high),
        "pct_from_52w_low": clean(pct_from_low),
        "bars": int(len(close)),
    }


def _scan_series(frame) -> dict | None:
    """A compact daily OHLCV series, alongside the derived metrics.

    The scan already downloads a full year of bars per symbol and used to
    throw them away once _scan_metrics reduced them to a handful of numbers
    -- the fetch was the expensive part, and it was happening for nothing
    the Go side kept. This is what lets the event study engine persist real
    price history for the whole scanned universe (1,000+ symbols) instead
    of only the ~86 that happen to have been charted or backtested.

    Columnar rather than one object per bar, and the date as a YYYYMMDD int
    rather than an ISO string: across ~1,250 symbols x ~250 trading days
    that is the difference between a response near the Go client's decode
    limit and one comfortably under it.
    """
    if frame is None or frame.empty:
        return None
    sub = frame.dropna(subset=["Open", "High", "Low", "Close"])
    if sub.empty:
        return None

    def clean(x) -> float | None:
        x = float(x)
        return round(x, 4) if math.isfinite(x) else None

    t, o, h, l, c, v = [], [], [], [], [], []
    for ts, row in sub.iterrows():
        if not isinstance(ts, pd.Timestamp):
            continue
        ov, hv, lv, cv = clean(row["Open"]), clean(row["High"]), clean(row["Low"]), clean(row["Close"])
        if ov is None or hv is None or lv is None or cv is None:
            continue
        vol = row.get("Volume", 0)
        vol = 0.0 if pd.isna(vol) else float(vol)
        t.append(int(ts.strftime("%Y%m%d")))
        o.append(ov)
        h.append(hv)
        l.append(lv)
        c.append(cv)
        v.append(round(vol, 2))
    if not t:
        return None
    return {"t": t, "o": o, "h": h, "l": l, "c": c, "v": v}


def scan(symbols: list[str]) -> dict:
    """Fetch a universe in batches and reduce each symbol to its metrics."""
    import yfinance as yf

    out: dict[str, dict] = {}
    series: dict[str, dict] = {}
    failed: list[str] = []

    for start in range(0, len(symbols), SCAN_CHUNK):
        chunk = symbols[start:start + SCAN_CHUNK]
        try:
            frame = yf.download(
                " ".join(chunk), period=SCAN_PERIOD, interval="1d",
                group_by="ticker", auto_adjust=True, progress=False,
                threads=True, timeout=30,
            )
        except Exception as exc:
            log.warning("scan chunk failed (%d symbols): %s", len(chunk), exc)
            failed.extend(chunk)
            continue

        for sym in chunk:
            try:
                sub = frame[sym] if len(chunk) > 1 else frame
                metrics = _scan_metrics(sub)
            except Exception:
                metrics = None
            if metrics is None:
                failed.append(sym)
                continue
            out[sym] = metrics
            try:
                s = _scan_series(sub)
            except Exception:
                s = None
            if s is not None:
                series[sym] = s

    return {"metrics": out, "series": series, "failed": failed,
            "as_of": datetime.now(timezone.utc).isoformat()}


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def _send(self, status: int, payload: dict) -> None:
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self) -> None:
        parsed = urlparse(self.path)
        query = parse_qs(parsed.query)

        if parsed.path == "/health":
            self._send(200, {"status": "ok", "yfinance": yf.__version__,
                             "time": datetime.now(timezone.utc).isoformat()})
            return

        if parsed.path == "/search":
            q = (query.get("q") or [""])[0].strip()
            try:
                limit = max(1, min(int((query.get("limit") or ["10"])[0]), 25))
            except ValueError:
                limit = 10
            if len(q) < 2:
                self._send(200, {"results": []})
                return
            try:
                self._send(200, {"results": search(q, limit)})
            except Exception as exc:
                log.warning("search failed for %r: %s", q, exc)
                self._send(502, {"error": f"{type(exc).__name__}: {exc}"[:200]})
            return

        if parsed.path != "/candles":
            self._send(404, {"error": "no such endpoint"})
            return

        symbol = (query.get("symbol") or [""])[0].strip()
        interval = (query.get("interval") or ["1d"])[0]
        adjust = (query.get("adjust") or ["false"])[0].lower() == "true"
        try:
            years = float((query.get("years") or ["5"])[0])
        except ValueError:
            years = 5.0

        if not symbol:
            self._send(400, {"error": "symbol is required"})
            return
        if interval not in INTERVALS:
            self._send(400, {"error": f"unsupported interval {interval!r}"})
            return

        try:
            payload = cached_fetch(symbol, interval, years, adjust)
        except Exception as exc:
            log.warning("fetch failed for %s: %s", symbol, exc)
            self._send(502, {"error": f"{type(exc).__name__}: {exc}"[:300]})
            return

        self._send(200, payload)


    def _do_fundamentals(self) -> None:
        try:
            length = int(self.headers.get("Content-Length") or "0")
        except ValueError:
            length = 0
        if length <= 0 or length > 200_000:
            self._send(400, {"error": "a symbol list is required"})
            return
        try:
            payload = json.loads(self.rfile.read(length) or b"{}")
        except json.JSONDecodeError:
            self._send(400, {"error": "body must be JSON"})
            return
        symbols = payload.get("symbols") or []
        if not isinstance(symbols, list) or not symbols:
            self._send(400, {"error": "symbols must be a non-empty list"})
            return
        # Fundamentals are far heavier per symbol than a price scan: each one
        # is several separate upstream requests. Kept small on purpose, and
        # the caller batches.
        if len(symbols) > 40:
            self._send(400, {"error": "at most 40 symbols per request"})
            return

        started = time.time()
        out, failed = {}, []
        for sym in symbols:
            try:
                out[sym] = fundamentals(str(sym))
            except Exception as exc:
                logging.warning("fundamentals failed for %s: %s", sym, exc)
                failed.append(str(sym))
        logging.info("fundamentals for %d symbols in %.1fs (%d failed)",
                     len(out), time.time() - started, len(failed))
        self._send(200, {
            "fundamentals": out,
            "failed": failed,
            "elapsed_seconds": round(time.time() - started, 2),
        })

    def do_POST(self) -> None:
        parsed = urlparse(self.path)
        if parsed.path == "/fundamentals":
            self._do_fundamentals()
            return
        if parsed.path != "/scan":
            self._send(404, {"error": "no such endpoint"})
            return

        try:
            length = int(self.headers.get("Content-Length") or "0")
        except ValueError:
            length = 0
        if length <= 0 or length > 2_000_000:
            self._send(400, {"error": "a symbol list is required"})
            return

        try:
            payload = json.loads(self.rfile.read(length))
            symbols = [str(s).strip() for s in payload.get("symbols") or [] if str(s).strip()]
        except Exception as exc:
            self._send(400, {"error": f"bad request body: {exc}"[:200]})
            return

        if not symbols:
            self._send(400, {"error": "a symbol list is required"})
            return
        if len(symbols) > 3000:
            self._send(400, {"error": "at most 3000 symbols per scan"})
            return

        started = time.time()
        try:
            result = scan(symbols)
        except Exception as exc:
            log.warning("scan failed: %s", exc)
            self._send(502, {"error": f"{type(exc).__name__}: {exc}"[:300]})
            return

        result["elapsed_seconds"] = round(time.time() - started, 2)
        log.info("scanned %d symbols in %.1fs (%d failed)",
                 len(symbols), result["elapsed_seconds"], len(result["failed"]))
        self._send(200, result)

    def log_message(self, fmt: str, *args) -> None:
        log.info("%s", fmt % args)


def main() -> None:
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    host = os.environ.get("YFIN_HOST", "0.0.0.0")
    port = int(os.environ.get("YFIN_PORT", "8787"))
    log.info("yfinance sidecar listening on %s:%d (yfinance %s)", host, port, yf.__version__)
    ThreadingHTTPServer((host, port), Handler).serve_forever()


if __name__ == "__main__":
    main()
