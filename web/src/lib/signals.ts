/**
 * What each scanner signal means, in words a reader already has.
 *
 * The scanner names its signals for the statistic behind them. The
 * interface names them for what they say about the stock.
 */
const SIGNALS: Record<string, string> = {
  volume_spike: "Unusual volume",
  price_move: "Large price move",
  gap: "Opened with a gap",
  near_52w_low: "Near its 52-week low",
  near_52w_high: "Near its 52-week high",
  volume_without_price: "Heavy volume, flat price",
};

export function signalName(s: string): string {
  const k = s.toLowerCase();
  if (SIGNALS[k]) return SIGNALS[k];
  const words = k.replace(/_/g, " ");
  return words.charAt(0).toUpperCase() + words.slice(1);
}

/** "Unusual volume and a large price move". */
export function signalSentence(signals: string[]): string {
  const names = signals.map(signalName);
  if (names.length === 0) return "Unusual activity";
  const lower = names.map((n, i) => (i === 0 ? n : n.charAt(0).toLowerCase() + n.slice(1)));
  if (lower.length === 1) return lower[0]!;
  return lower.slice(0, -1).join(", ") + " and " + lower[lower.length - 1];
}

export function signed(value: number, digits = 2): string {
  return `${value > 0 ? "+" : value < 0 ? "−" : ""}${Math.abs(value).toFixed(digits)}`;
}

export function toneOf(value: number): string {
  return value > 0 ? "text-semantic-up" : value < 0 ? "text-semantic-down" : "text-text-secondary";
}
