/**
 * Typed client for the TradeSys API.
 *
 * Every response the backend can produce is modelled here, including its
 * provenance fields — `source`, `stale`, `resolved_symbol` — because the UI is
 * required to tell operators where a number came from rather than presenting
 * cached or substituted data as live.
 */

export interface ApiErrorBody {
  error: { code: string; message: string };
}

/** ApiError carries the backend's error code so callers can branch on it. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }

  /** True when the operator is not authenticated. */
  get isUnauthorized(): boolean {
    return this.status === 401;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let res: Response;
  try {
    res = await fetch(path, {
      ...init,
      headers: {
        Accept: "application/json",
        ...(init?.body ? { "Content-Type": "application/json" } : {}),
        ...init?.headers,
      },
    });
  } catch (cause) {
    // A network-level failure: the server is down, or the browser is offline.
    throw new ApiError(0, "network_error", "Could not reach the server.");
  }

  if (res.status === 204) {
    return undefined as T;
  }

  const text = await res.text();
  let body: unknown;
  try {
    body = text ? JSON.parse(text) : {};
  } catch {
    throw new ApiError(res.status, "bad_response", "The server sent a malformed response.");
  }

  if (!res.ok) {
    const err = body as Partial<ApiErrorBody> & {
      error?: { fields?: FieldError[] };
    };
    const message = err.error?.message ?? `Request failed with status ${res.status}.`;
    // A validation failure carries per-field messages so the builder can show
    // each one inline next to the input that caused it.
    if (err.error?.fields?.length) {
      throw new ValidationError(res.status, message, err.error.fields);
    }
    throw new ApiError(res.status, err.error?.code ?? "unknown_error", message);
  }
  return body as T;
}

export type Interval = "1m" | "5m" | "15m" | "1h" | "1d" | "1wk";

export interface Meta {
  app: string;
  version: string;
  display_tz: string;
  disclaimer: string;
  server_time: string;
  features: {
    ai: boolean;
    telegram: boolean;
    search: boolean;
    alphavantage: boolean;
  };
  marketdata_providers: string[];
  intervals: Interval[];
}

export type HealthStatus = "ok" | "degraded" | "down" | "unconfigured";

export interface ProviderHealth {
  provider: string;
  kind: string;
  status: HealthStatus;
  message: string;
  last_ok_at: string | null;
  last_error_at: string | null;
  updated_at: string;
}

export interface BudgetView {
  used: number;
  limit: number;
  exhausted: boolean;
}

export interface HealthResponse {
  providers: ProviderHealth[];
  /** Every configured provider is failing, so prices may be stale. */
  market_data_degraded: boolean;
  /** Providers that are failing, even where a fallback is covering them. */
  degraded_providers: string[];
  budgets: Record<string, BudgetView>;
  checked_at: string;
}

export interface WatchlistItem {
  symbol: string;
  ticker: string;
  exchange: string;
  currency: string;
  note: string;
  added_at: string;
  price?: number;
  change?: number;
  change_percent?: number;
  spark?: number[];
  source?: string;
  resolved_symbol?: string;
  stale: boolean;
  error?: string;
}

export interface Candle {
  /** Bar open time, RFC3339 UTC. */
  t: string;
  o: number;
  h: number;
  l: number;
  c: number;
  v: number;
}

export interface CandlesResponse {
  symbol: string;
  interval: Interval;
  currency: string;
  candles: Candle[];
  source: string;
  resolved_symbol?: string;
  fetched_at: string;
  stale: boolean;
}

export interface Quote {
  price: number;
  prev_close: number;
  change: number;
  change_percent: number;
  day_high: number;
  day_low: number;
  volume: number;
  currency: string;
  as_of: string;
}

export interface QuoteResponse {
  symbol: string;
  currency: string;
  quote: Quote;
  source: string;
  fetched_at: string;
  stale: boolean;
}

// ---------------------------------------------------------------------------
// Algorithms
// ---------------------------------------------------------------------------

export type Operator =
  | "<" | "<=" | ">" | ">=" | "==" | "!="
  | "crosses_above" | "crosses_below";

/** One side of a comparison. */
export interface Operand {
  indicator: string;
  period?: number;
  fast?: number;
  slow?: number;
  signal?: number;
  mult?: number;
  offset?: number;
  /** Read the value this many bars back; 1 means "as it stood yesterday". */
  shift?: number;
}

/**
 * A condition group member: either a nested group (`all`/`any`) or a leaf
 * comparison whose left-hand operand is flattened into the node.
 */
export interface Node {
  all?: Node[];
  any?: Node[];

  indicator?: string;
  period?: number;
  fast?: number;
  slow?: number;
  signal?: number;
  shift?: number;

  op?: Operator;
  value?: number;
  compare?: Operand;
}

export interface NotifyConfig {
  telegram: boolean;
  ai_context: boolean;
}

export interface Algorithm {
  id?: number;
  name: string;
  symbols: string[];
  /**
   * Lists this rule is attached to. Their members are resolved at every run
   * and unioned with `symbols`, so a rule can name no symbols at all and
   * still evaluate whatever is on the list at the time.
   */
  watchlist_ids?: number[];
  interval: Interval;
  all?: Node[];
  any?: Node[];
  cooldown_hours: number;
  notify: NotifyConfig;
  enabled: boolean;
  created_at?: string;
  updated_at?: string;
  /** Derived by the backend: how much history this algorithm needs. */
  required_bars?: number;
}

export interface IndicatorInfo {
  name: string;
  params: "none" | "period" | "macd";
  default_period?: number;
}

export interface Vocabulary {
  indicators: IndicatorInfo[];
  operators: Operator[];
  intervals: Interval[];
}

export interface Template {
  key: string;
  title: string;
  description: string;
  rationale: string;
  algorithm: Algorithm;
}

export interface ConditionResult {
  label: string;
  op: string;
  left_value: number;
  right_label: string;
  right_value: number;
  result: "true" | "false" | "unknown";
  detail?: string;
  text: string;
}

export type EvalStatus = "triggered" | "not_met" | "insufficient_data" | "error";

export interface EvalResult {
  symbol: string;
  interval: string;
  status: EvalStatus;
  bar_time: string;
  price: number;
  conditions: ConditionResult[];
  summary: string;
  reason?: string;
}

export interface PreviewResult {
  required_bars: number;
  evaluated_at: string;
  results: Array<{
    symbol: string;
    result?: EvalResult;
    error?: string;
    source?: string;
    stale: boolean;
  }>;
}

/** Per-field validation messages, keyed by JSON path such as "all[1].period". */
export interface FieldError {
  field: string;
  message: string;
}

/** ValidationError carries the per-field messages the builder shows inline. */
export class ValidationError extends ApiError {
  readonly fields: FieldError[];

  constructor(status: number, message: string, fields: FieldError[]) {
    super(status, "invalid_algorithm", message);
    this.name = "ValidationError";
    this.fields = fields;
  }

  /** Looks up the message for one field path. */
  forField(path: string): string | undefined {
    return this.fields.find((f) => f.field === path)?.message;
  }
}

// ---------------------------------------------------------------------------
// Alerts
// ---------------------------------------------------------------------------

export interface DeliveryRecord {
  channel: string;
  ok: boolean;
  error?: string;
  at: string;
}

export interface Alert {
  id: number;
  algorithm_id: number;
  algorithm_name: string;
  symbol: string;
  interval: string;
  fired_at: string;
  bar_time: string;
  price: number;
  summary: string;
  conditions: ConditionResult[];
  ai_context?: string;
  ai_status?: string;
  delivery: DeliveryRecord[];
  read_at?: string;
}

export interface AlertPage {
  alerts: Alert[];
  unread: number;
}

// ---------------------------------------------------------------------------
// AI
// ---------------------------------------------------------------------------

/**
 * Why an AI feature has no output.
 *
 * The distinction matters to the reader: "not set up" and "budget spent" are
 * states they can act on, while "unavailable" is a fault. Collapsing them into
 * one error message would make a configuration gap look like a bug.
 */
export type AIStatus = "ok" | "unconfigured" | "budget_reached" | "unavailable";

export interface BudgetState {
  period: string;
  used: number;
  limit: number;
  exhausted: boolean;
  by_feature?: Record<string, number>;
}

export interface AIStatusResponse {
  status: AIStatus;
  budget: BudgetState;
  model?: string;
  cheap_model?: string;
  search: boolean;
}

export interface BriefCoverage {
  symbols: number;
  articles: number;
  alerts: number;
}

export interface MorningBrief {
  date: string;
  generated_at: string;
  model: string;
  status: AIStatus;
  headline: string;
  bullets: string[];
  watch_today: string[];
  coverage: BriefCoverage;
}

export interface Citation {
  index: number;
  title: string;
  url: string;
  source: string;
  snippet?: string;
  /** Whether the model actually referenced this source. */
  used: boolean;
}

export interface Explanation {
  symbol: string;
  generated_at: string;
  model: string;
  status: AIStatus;
  price: number;
  change_percent: number;
  explanation: string;
  confidence: "high" | "medium" | "low";
  caveat: string;
  citations: Citation[] | null;
  note?: string;
}

export interface Scenario {
  probability: number;
  move_percent: number;
  reasoning: string;
}

export interface Outlook {
  id: number;
  symbol: string;
  created_at: string;
  horizon_days: number;
  price_at_creation: number;
  base: Scenario;
  bull: Scenario;
  bear: Scenario;
  key_risk: string;
  model: string;
  resolved_at?: string;
  realized_price?: number;
  realized_move_percent?: number;
  actual_scenario?: "base" | "bull" | "bear";
  brier_score?: number;
}

export interface CalibrationBucket {
  lower: number;
  upper: number;
  forecasts: number;
  occurred: number;
  mean_stated: number;
  observed_rate: number;
}

export interface Calibration {
  buckets: CalibrationBucket[];
  resolved: number;
  pending: number;
  mean_brier: number;
  baseline_brier: number;
  generated_at: string;
}

export interface CalcField {
  label: string;
  value: string;
  raw: number;
}

export interface SymbolSearchResult {
  symbol: string;
  ticker: string;
  name: string;
  exchange: string;
  currency: string;
  type: string;
}


/* ── Events: the news and filings layer ─────────────────────────────────── */

/**
 * One instrument's exposure to an event.
 *
 * `direction` is per company rather than per event on purpose: the same event
 * points opposite ways for different companies, so a single event-level
 * sentiment would have to pick one and be wrong for the other.
 */
export interface EventEntity {
  symbol: string;
  relationship: "primary" | "mentioned" | "peer" | "sector";
  match_confidence: number;
  match_method: string;
  direction?: "positive" | "negative" | "unclear";
  impact_strength?: number;
  rationale?: string;
}

/** A piece of evidence: one raw item that supports an event. */
export interface Evidence {
  id: number;
  source_id: string;
  url: string;
  canonical_url: string;
  title: string;
  description?: string;
  publisher: string;
  /** Words of article text actually read. Absent when only a headline was
   *  available, which is worth showing: a source that was read is stronger
   *  evidence than one that was merely listed. */
  words?: number;
  published_at?: string;
  discovered_at: string;
}

/**
 * A market event.
 *
 * This is the central object, not the article. Eleven outlets reporting one
 * acquisition is one event with eleven pieces of evidence.
 */
export interface MarketEvent {
  id: number;
  event_type: string;
  headline: string;
  summary?: string;
  occurred_at?: string;
  published_at?: string;
  discovered_at: string;
  confirmed_at?: string;
  updated_at: string;
  importance?: number;
  confidence?: number;
  best_trust: number;
  source_count: number;
  official: boolean;
  classified_at?: string;
  model?: string;
  entities?: EventEntity[];
  evidence?: Evidence[];
  sectors?: string[];
  facts?: Record<string, string>;
  age_label: string;
  /** How old the content is, as distinct from when it was discovered. */
  staleness?: "fresh" | "recent" | "old" | "unknown";
  /** Best URL among the evidence, so a row can link out without expanding. */
  primary_url?: string;
  /** A short AI note on what the item means. Absent until one is asked for. */
  brief?: string;
  /** Whether published_at may be presented as a publication time. */
  timestamp_trust?: string;
  /** Seconds between the publisher's timestamp and our discovery of it. */
  latency_seconds?: number;
}

export interface EventListResponse {
  events: MarketEvent[];
  total: number;
  limit: number;
  offset: number;
}

/** One House Clerk STOCK Act disclosure -- see internal/congress and internal/server/congress.go. */
export interface CongressFiling {
  doc_id: string;
  chamber: string;
  member: string;
  state_district: string;
  filing_type: string;
  filing_date: string;
  symbols: string[];
  unresolved_tickers?: string[];
  earliest_transaction_date?: string;
  disclosure_delay_days?: number;
  doc_url: string;
}

/** One event type's population size in the archive -- internal/storage/postgres/eventstudy.go. */
export interface EventTypeCount {
  event_type: string;
  count: number;
}

/**
 * Does this event type actually move the stocks it names, historically --
 * see internal/eventstudy's package doc for the method. Every abnormal
 * return is against the symbol's own venue benchmark (S&P 500 or NIFTY 50),
 * not a fixed zero line.
 */
export interface EventStudyResult {
  event_type: string;
  benchmark: string;
  holding_days: number;
  total_events: number;
  samples: number;
  mean_abnormal_return_pct: number;
  median_abnormal_return_pct: number;
  stddev_pct: number;
  hit_rate: number;
  warnings?: string[];
}

export interface EventQuery {
  symbol?: string;
  q?: string;
  type?: string;
  minImportance?: number;
  /** "all" widens the feed from index constituents to every listed company. */
  universe?: "index" | "all";
  hours?: number;
  official?: boolean;
  /**
   * "published" (the default) sorts by when the publisher dated the item;
   * "arrival" by when we found it, which is what an audit of the ingestion
   * wants and what a reader almost never does.
   */
  order?: "published" | "arrival";
  limit?: number;
  offset?: number;
}

/* ── Ingestion health ───────────────────────────────────────────────────── */

export interface IngestSource {
  id: string;
  name: string;
  category: string;
  method: string;
  country?: string;
  trust: number;
  usage: string;
  display: string;
  official: boolean;
  refresh_ms: number;
  healthy: boolean;
  consecutive_failures: number;
  last_error?: string;
  last_success_at?: string;
  last_attempt_at?: string;
  total_attempts: number;
  total_items: number;
  total_new_items: number;
}

export interface HotEntry {
  key: string;
  score: number;
  heat: "normal" | "warm" | "hot";
  reason?: string;
  since?: string;
}

/* ── Deep research ──────────────────────────────────────────────────────── */

export interface ResearchSource {
  /** Words of article text actually read. Absent when only the headline
   *  could be retrieved, which is worth showing: a source that was read is
   *  stronger evidence than one that was merely listed. */
  words?: number;
  title: string;
  url: string;
  publisher: string;
  snippet?: string;
  published_at?: string;
  scraper: string;
  trust: number;
  symbols?: string[];
}

export interface ResearchScraperReport {
  name: string;
  count: number;
  error?: string;
}

export interface DebriefSection {
  title: string;
  body: string;
  events?: number[];
}

export interface DebriefOutstanding {
  item: string;
  expected?: string;
  events?: number[];
}

export interface SymbolDebrief {
  symbol: string;
  company?: string;
  period: string;
  headline: string;
  sections?: DebriefSection[];
  outstanding?: DebriefOutstanding[];
  blind_spots?: string[];
  event_count: number;
  official_count: number;
  model?: string;
  generated_at: string;
  events?: MarketEvent[];
}

export interface ResearchClaim {
  claim: string;
  sources: number[];
  confidence?: string;
}

export interface ResearchCompanyRef {
  symbol: string;
  relevance?: string;
  direction?: string;
  sources?: number[];
}

export interface ResearchStep {
  at: string;
  stage: string;
  detail?: string;
}

export interface MarketReturn {
  horizon: string;
  percent: number;
  from: number;
  from_date: string;
}

export interface MarketStats {
  symbol: string;
  as_of: string;
  close: number;
  returns?: MarketReturn[];
  volatility_percent: number;
  max_drawdown_percent: number;
  high_52w: number;
  low_52w: number;
  pct_from_52w_high: number;
  pct_from_52w_low: number;
  avg_volume: number;
  last_volume: number;
  volume_ratio: number;
  bars: number;
}

export interface FundamentalsSnapshot {
  symbol: string;
  as_of: string;
  sector?: string;
  industry?: string;
  quote_currency?: string;
  financial_currency?: string;
  pe_trailing?: number;
  pe_forward?: number;
  price_to_book?: number;
  market_cap?: number;
  ev_to_ebitda?: number;
  return_on_equity?: number;
  operating_margin?: number;
  profit_margin?: number;
  debt_to_equity?: number;
  dividend_yield?: number;
  eps_trailing?: number;
  book_value?: number;
  revenue_growth?: number;
  earnings_growth?: number;
  beta?: number;
}

export interface PeerStat {
  metric: string;
  value?: number;
  median?: number;
  percentile?: number;
  peers: number;
  higher_is_better: boolean;
  /** How to read the number: "ratio", "percent" (already scaled), or
   *  "fraction" (multiply by 100). Stated by the server because the upstream
   *  provider is inconsistent — return on equity arrives as 0.32 for 32%,
   *  dividend yield as 4.37 for 4.37% — and it cannot be inferred here. */
  unit?: "ratio" | "percent" | "fraction";
}

export interface FundamentalsTrend {
  metric: string;
  points: number[];
  change_percent?: number;
  direction: "improving" | "deteriorating" | "flat";
  from?: string;
  to?: string;
  gaps?: number;
}

export interface FinancialPeriod {
  period_end: string;
  period_type: string;
  report_date?: string;
  revenue?: number;
  net_income?: number;
  ebitda?: number;
  operating_income?: number;
  total_debt?: number;
  eps_diluted?: number;
}

export interface Fundamentals {
  symbol: string;
  snapshot: FundamentalsSnapshot;
  /** True when statements are filed in a different currency from the one the
   *  share trades in. Ratios mixing the two are withheld rather than shown in
   *  mixed units. */
  mixed_currency?: boolean;
  quarterly?: FinancialPeriod[];
  annual?: FinancialPeriod[];
  trends?: FundamentalsTrend[];
  peers?: { symbol: string; industry: string; peer_count: number; stats: PeerStat[] } | null;
}

export interface ResearchSection {
  heading: string;
  body: string;
  sources?: number[];
}

export interface ResearchTurn {
  id: number;
  conversation_id: number;
  seq: number;
  question: string;
  /** What was actually searched. For a follow-up this is the rewritten query. */
  search_query?: string;
  answer: string;
  /** The body of the report, beneath the summary. */
  sections?: ResearchSection[];
  findings?: ResearchClaim[];
  companies?: ResearchCompanyRef[];
  gaps?: string[];
  followups?: string[];
  sources?: ResearchSource[];
  /** Computed from the price series rather than retrieved. Survives a failed
   *  synthesis, because arithmetic does not depend on the model. */
  measurements?: MarketStats[];
  providers?: ResearchScraperReport[];
  model?: string;
  degraded: boolean;
  note?: string;
  elapsed_ms: number;
  created_at: string;
  status: "running" | "done" | "failed";
  stage?: string;
  progress?: ResearchStep[];
  started_at?: string;
  finished_at?: string;
  error?: string;
}

export interface Conversation {
  id: number;
  title: string;
  created_at: string;
  updated_at: string;
  turn_count: number;
  symbols?: string[];
  archived: boolean;
  turns?: ResearchTurn[];
}


/** One instrument the scanner considers to be behaving abnormally. */
export interface ScanFinding {
  id: number;
  scan_id: number;
  as_of: string;
  scanned_at: string;
  symbol: string;
  signals: string[];
  score: number;
  close: number;
  return_1d: number;
  return_5d: number;
  return_z: number;
  volume: number;
  volume_ratio: number;
  volume_z: number;
  gap_percent: number;
  pct_from_52w_high: number;
  pct_from_52w_low: number;
  /** Whether the archive already accounted for the move. Absent means the
   *  question was never asked, which is not the same as "no". */
  explained?: boolean;
  explained_at?: string;
}

export interface ScanResult {
  as_of: string;
  universe: number;
  scanned: number;
  failed: number;
  elapsed: string;
  findings: ScanFinding[];
}

/** Who a key belongs to. */
export interface KeyProfile {
  id: number;
  /** The public half of the key, safe to display. */
  prefix: string;
  name: string;
  role: "owner" | "operator" | "viewer";
  note?: string;
  created_at: string;
  last_used_at?: string;
  revoked_at?: string;
}

// ---------------------------------------------------------------- watchlists

/** A named list. `count` is the number of instruments on it. */
export interface Watchlist {
  id: number;
  name: string;
  position: number;
  count: number;
  created_at: string;
}

/**
 * The outcome of a bulk add.
 *
 * Three buckets rather than a count, because "12 added" hides the interesting
 * half: `skipped` was already on the list, `rejected` was not a ticker at all.
 * Conflating them shows the operator the wrong message for both.
 */
export interface BulkAddResult {
  added: string[];
  skipped: string[];
  rejected: string[];
}

// ------------------------------------------------------------------- screens

/** A measurement a screen condition can be written about. */
export interface ScreenField {
  field: string;
  label: string;
  unit: string;
}

export interface ScreenOp {
  op: string;
  label: string;
}

export interface ScreenCatalogue {
  fields: ScreenField[];
  ops: ScreenOp[];
  max_conditions: number;
  max_limit: number;
}

export interface ScreenCondition {
  field: string;
  op: string;
  value: number;
}

export interface ScreenDefinition {
  match: "all" | "any";
  conditions: ScreenCondition[];
  watchlist_ids?: number[];
  sort_by: string;
  sort_desc: boolean;
  limit: number;
}

export interface ScreenRow {
  symbol: string;
  industry: string;
  close: number;
  return_1d: number;
  return_5d: number;
  return_z: number;
  volume: number;
  volume_ratio: number;
  volume_z: number;
  gap_percent: number;
  pct_from_52w_high: number;
  pct_from_52w_low: number;
  bars: number;
  signals: string[];
}

export interface ScreenResult {
  rows: ScreenRow[];
  /** How many instruments the underlying scan measured. */
  universe: number;
  scan_as_of: string;
  elapsed: string;
}

export interface Screen {
  id: number;
  name: string;
  description: string;
  definition: ScreenDefinition;
  created_at: string;
  updated_at: string;
  last_run_at?: string;
}

// ----------------------------------------------------------------- backtest

export interface BacktestConfig {
  /** Reads the rule as an entry to buy or to sell. */
  direction: "long" | "short";
  stop_loss_pct: number;
  take_profit_pct: number;
  /** Counted from the entry bar inclusive. */
  max_hold_bars: number;
  /** Charged on entry and again on exit, in basis points. */
  cost_bps: number;
  /**
   * How much of the account a position uses. `risk` sizes so that being
   * stopped out costs exactly `risk_pct`, which requires a stop.
   */
  size_mode: "full" | "fraction" | "risk";
  size_pct: number;
  risk_pct: number;
  /** Optional second rule that closes a position when it holds. */
  exit_rule?: Algorithm | null;
}

export interface BacktestTrade {
  direction: "long" | "short";
  /** Share of the account committed, 0-1. */
  size: number;
  entry_time: string;
  entry_price: number;
  exit_time: string;
  exit_price: number;
  bars: number;
  /** Return on the notional committed. */
  return_pct: number;
  /** What the trade did to the account: return_pct scaled by size. */
  account_pct: number;
  reason: string;
  /** The bar hit both the stop and the target; the stop was assumed. */
  ambiguous?: boolean;
}

export interface BacktestStats {
  trades: number;
  wins: number;
  losses: number;
  win_rate: number;
  total_pct: number;
  cagr: number;
  avg_win_pct: number;
  avg_loss_pct: number;
  profit_factor: number;
  max_drawdown: number;
  sharpe: number;
  exposure_pct: number;
  avg_bars: number;
  buy_hold_pct: number;
  ambiguous: number;
}

export interface BacktestEquityPoint {
  t: string;
  e: number;
  h: number;
}

export interface BacktestResult {
  symbol: string;
  interval: Interval;
  from: string;
  to: string;
  bars: number;
  stats: BacktestStats;
  trades: BacktestTrade[];
  equity: BacktestEquityPoint[];
  warnings?: string[];
}

export interface BacktestSummary {
  symbols: number;
  trades: number;
  win_rate: number;
  avg_total_pct: number;
  avg_buy_hold_pct: number;
  beat: number;
  worst_drawdown: number;
  median_total_pct: number;
  median_buy_hold_pct: number;
  total_ambiguous: number;
  symbols_no_trades: number;
}

export interface BacktestResponse {
  results: BacktestResult[];
  summary: BacktestSummary;
  skipped?: { symbol: string; reason: string }[];
  elapsed: string;
}

export const api = {
  /** Whether a session is required, whether this browser has one, and whose. */
  authStatus: () =>
    request<{
      required: boolean;
      authenticated: boolean;
      profile?: KeyProfile;
      note?: string;
    }>("/api/auth/status"),

  login: (key: string) =>
    request<{ authenticated: boolean; profile: KeyProfile }>("/api/auth/login", {
      method: "POST",
      body: JSON.stringify({ key }),
    }),

  logout: () => request<{ authenticated: boolean }>("/api/auth/logout", { method: "POST" }),

  meta: () => request<Meta>("/api/meta"),

  health: () => request<HealthResponse>("/api/health"),

  watchlist: () => request<{ items: WatchlistItem[] }>("/api/watchlist"),

  addWatchlist: (symbol: string, note = "") =>
    request<{ symbol: string }>("/api/watchlist", {
      method: "POST",
      body: JSON.stringify({ symbol, note }),
    }),

  removeWatchlist: (symbol: string) =>
    request<void>(`/api/watchlist/${encodeURIComponent(symbol)}`, { method: "DELETE" }),

  candles: (symbol: string, interval: Interval, limit = 300) =>
    request<CandlesResponse>(
      `/api/symbols/${encodeURIComponent(symbol)}/candles?interval=${interval}&limit=${limit}`,
    ),

  quote: (symbol: string) =>
    request<QuoteResponse>(`/api/symbols/${encodeURIComponent(symbol)}/quote`),

  // Algorithms
  vocabulary: () => request<Vocabulary>("/api/algorithms/vocabulary"),

  templates: () => request<{ templates: Template[] }>("/api/algorithms/templates"),


  /* ── Events ── */

  /**
   * Writes a short "what happened and why it matters" for one item.
   *
   * On demand, not on arrival: the feed takes thousands of items a day and
   * almost all are procedural. The result is kept, so the second reader does
   * not pay for it again.
   */
  briefEvent: (id: number) =>
    request<{ brief: string; cached: boolean }>(`/api/events/${id}/brief`, { method: "POST" }),

  /** The catalogue of event types, for building a filter from. */
  eventTypes: () => request<{ types: { type: string; label?: string }[] }>("/api/events/types"),

  events: (q: EventQuery = {}) => {
    const p = new URLSearchParams();
    if (q.symbol) p.set("symbol", q.symbol);
    if (q.q) p.set("q", q.q);
    if (q.type) p.set("type", q.type);
    if (q.minImportance) p.set("min_importance", String(q.minImportance));
    if (q.universe === "all") p.set("universe", "all");
    if (q.hours !== undefined) p.set("hours", String(q.hours));
    if (q.official) p.set("official", "1");
    if (q.order === "arrival") p.set("order", "arrival");
    if (q.limit) p.set("limit", String(q.limit));
    if (q.offset) p.set("offset", String(q.offset));
    const qs = p.toString();
    return request<EventListResponse>(`/api/events${qs ? `?${qs}` : ""}`);
  },

  event: (id: number) => request<MarketEvent>(`/api/events/${id}`),

  /**
   * Each symbol's industry, spelled to match an event's own `sectors`
   * directly (an "US: " prefix for a US/GICS symbol, the bare NSE industry
   * name otherwise) -- see internal/server/events.go's handleSymbolSectors.
   */
  symbolSectors: (symbols: string[]) => {
    if (symbols.length === 0) return Promise.resolve({ sectors: {} as Record<string, string> });
    const p = new URLSearchParams({ symbols: symbols.join(",") });
    return request<{ sectors: Record<string, string> }>(`/api/symbols/sectors?${p.toString()}`);
  },

  /**
   * Recent House Clerk STOCK Act disclosures, optionally narrowed to one
   * symbol -- see internal/server/congress.go. Document-level, not
   * row-level: `symbols` is everything a filing names, not a per-trade
   * breakdown (internal/congress's package doc explains why the source
   * PDFs do not support that split reliably).
   */
  congressFilings: (opts: { symbol?: string; limit?: number } = {}) => {
    const p = new URLSearchParams();
    if (opts.symbol) p.set("symbol", opts.symbol);
    if (opts.limit) p.set("limit", String(opts.limit));
    const qs = p.toString();
    return request<{ filings: CongressFiling[] }>(`/api/congress/filings${qs ? `?${qs}` : ""}`);
  },

  /** Event types the archive actually holds events under, most populous first. */
  eventStudyTypes: () => request<{ types: EventTypeCount[] }>("/api/eventstudy/types"),

  /** Does this event type actually move the stocks it names -- internal/eventstudy. */
  eventStudy: (type: string, days = 5) => {
    const p = new URLSearchParams({ type, days: String(days) });
    return request<EventStudyResult>(`/api/eventstudy?${p.toString()}`);
  },


  /** Valuation, reported periods and peer position for one company. */
  fundamentals: (symbol: string) =>
    request<Fundamentals>(`/api/symbols/${encodeURIComponent(symbol)}/fundamentals`),

  /** The most recent scan's findings. */
  /** What the scanner has said about one instrument over time. */
  scanHistory: (symbol: string, limit = 12) =>
    request<{ symbol: string; findings: ScanFinding[] | null; count: number }>(
      `/api/scan/symbols/${encodeURIComponent(symbol)}?limit=${limit}`,
    ),

  scanLatest: (limit = 40) =>
    request<{ findings: ScanFinding[] | null; count: number }>(
      `/api/scan/latest?limit=${limit}`,
    ),


  /** Runs a scan now. Takes around two minutes across the full universe. */
  runScan: () => request<ScanResult>("/api/scan/run", { method: "POST" }),


  /** Starts a research turn. Returns as soon as it is queued; poll the
   *  conversation for progress. */
  ask: (question: string, conversationId?: number, perProvider = 30) =>
    request<{ conversation_id: number; turn: ResearchTurn }>("/api/research/ask", {
      method: "POST",
      body: JSON.stringify({
        question,
        conversation_id: conversationId ?? 0,
        per_provider: perProvider,
      }),
    }),

  conversations: () =>
    request<{ conversations: Conversation[] }>("/api/research/conversations"),

  conversation: (id: number) => request<Conversation>(`/api/research/conversations/${id}`),

  deleteConversation: (id: number) =>
    request<void>(`/api/research/conversations/${id}`, { method: "DELETE" }),

  algorithms: () => request<{ algorithms: Algorithm[] }>("/api/algorithms"),

  algorithm: (id: number) => request<Algorithm>(`/api/algorithms/${id}`),

  createAlgorithm: (a: Algorithm) =>
    request<Algorithm>("/api/algorithms", { method: "POST", body: JSON.stringify(a) }),

  updateAlgorithm: (id: number, a: Algorithm) =>
    request<Algorithm>(`/api/algorithms/${id}`, { method: "PUT", body: JSON.stringify(a) }),

  deleteAlgorithm: (id: number) =>
    request<void>(`/api/algorithms/${id}`, { method: "DELETE" }),

  previewAlgorithm: (a: Algorithm) =>
    request<PreviewResult>("/api/algorithms/preview", {
      method: "POST",
      body: JSON.stringify(a),
    }),



  // Alerts
  alerts: (params: { limit?: number; unread?: boolean; symbol?: string; algorithmId?: number } = {}) => {
    const q = new URLSearchParams();
    if (params.limit) q.set("limit", String(params.limit));
    if (params.unread) q.set("unread", "true");
    if (params.symbol) q.set("symbol", params.symbol);
    if (params.algorithmId) q.set("algorithm_id", String(params.algorithmId));
    const suffix = q.toString() ? `?${q}` : "";
    return request<AlertPage>(`/api/alerts${suffix}`);
  },


  markAllAlertsRead: () => request<void>("/api/alerts/read-all", { method: "POST" }),

  searchSymbols: (q: string, limit = 8) =>
    request<{ results: SymbolSearchResult[] }>(
      `/api/symbols/search?q=${encodeURIComponent(q)}&limit=${limit}`,
    ),

  // AI
  aiStatus: () => request<AIStatusResponse>("/api/ai/status"),







  explainMove: (symbol: string) =>
    request<Explanation>(`/api/symbols/${encodeURIComponent(symbol)}/explain`, { method: "POST" }),


  outlooks: (symbol?: string, limit = 50) => {
    const q = new URLSearchParams({ limit: String(limit) });
    const path = symbol
      ? `/api/symbols/${encodeURIComponent(symbol)}/outlooks?${q}`
      : `/api/ai/outlooks?${q}`;
    return request<{ outlooks: Outlook[] }>(path);
  },

  calibration: () => request<Calibration>("/api/ai/calibration"),




  debrief: (symbol: string, days = 30) =>
    request<SymbolDebrief>(
      `/api/symbols/${encodeURIComponent(symbol)}/debrief?days=${days}`,
      { method: "POST" },
    ),

  symbolEvents: (symbol: string, limit = 30) =>
    request<EventListResponse>(
      `/api/symbols/${encodeURIComponent(symbol)}/events?limit=${limit}`,
    ),

  // Watchlists — up to five named lists.
  watchlists: () =>
    request<{ watchlists: Watchlist[]; max: number }>("/api/watchlists"),

  createWatchlist: (name: string) =>
    request<Watchlist>("/api/watchlists", {
      method: "POST",
      body: JSON.stringify({ name }),
    }),

  renameWatchlist: (id: number, name: string) =>
    request<void>(`/api/watchlists/${id}`, {
      method: "PUT",
      body: JSON.stringify({ name }),
    }),

  deleteWatchlist: (id: number) =>
    request<void>(`/api/watchlists/${id}`, { method: "DELETE" }),

  watchlistItems: (id: number) =>
    request<{ items: WatchlistItem[] }>(`/api/watchlists/${id}/symbols`),

  /**
   * Adds many at once.
   *
   * A string is a pasted block and goes to `text`, which the server splits on
   * commas, newlines, tabs and semicolons — deliberately not spaces. An array
   * is already-separated symbols and is passed through untouched. Sending a
   * paste as a one-element array made the server treat the whole block as a
   * single ticker and reject it wholesale.
   */
  addToWatchlist: (id: number, input: string[] | string) =>
    request<BulkAddResult>(`/api/watchlists/${id}/symbols`, {
      method: "POST",
      body: JSON.stringify(
        typeof input === "string" ? { text: input } : { symbols: input },
      ),
    }),

  removeFromWatchlist: (id: number, symbol: string) =>
    request<void>(
      `/api/watchlists/${id}/symbols/${encodeURIComponent(symbol)}`,
      { method: "DELETE" },
    ),

  /** Copies every instrument from one list onto another. Additive. */
  copyWatchlist: (from: number, to: number) =>
    request<{ copied: number }>(`/api/watchlists/${from}/copy`, {
      method: "POST",
      body: JSON.stringify({ to }),
    }),

  // Screens — custom scanners over the full NSE constituent universe.
  screenCatalogue: () => request<ScreenCatalogue>("/api/screens/fields"),

  screens: () => request<{ screens: Screen[] }>("/api/screens"),

  /** Runs a definition without saving it. Used while the builder is open. */
  runScreen: (def: ScreenDefinition) =>
    request<ScreenResult>("/api/screens/run", {
      method: "POST",
      body: JSON.stringify(def),
    }),

  createScreen: (name: string, description: string, definition: ScreenDefinition) =>
    request<Screen>("/api/screens", {
      method: "POST",
      body: JSON.stringify({ name, description, definition }),
    }),

  updateScreen: (id: number, name: string, description: string, definition: ScreenDefinition) =>
    request<Screen>(`/api/screens/${id}`, {
      method: "PUT",
      body: JSON.stringify({ name, description, definition }),
    }),

  deleteScreen: (id: number) =>
    request<void>(`/api/screens/${id}`, { method: "DELETE" }),

  runSavedScreen: (id: number) =>
    request<ScreenResult>(`/api/screens/${id}/run`, { method: "POST" }),

  /**
   * Runs a rule over history.
   *
   * Slow by nature — the evaluator is re-run at every bar so that look-ahead
   * is impossible — so callers should expect seconds, not milliseconds, and
   * more of them as the window or the symbol count grows.
   */
  backtest: (body: {
    algorithm: Algorithm;
    symbols?: string[];
    watchlist_ids?: number[];
    bars: number;
    config: BacktestConfig;
  }) =>
    request<BacktestResponse>("/api/algorithms/backtest", {
      method: "POST",
      body: JSON.stringify(body),
    }),

  /** Every configured feed and how it is behaving. */
  ingestSources: () =>
    request<{ sources: IngestSource[] }>("/api/ingest/sources"),
};
