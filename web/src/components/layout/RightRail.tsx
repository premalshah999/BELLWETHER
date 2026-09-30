import { Fundamentals } from "../Fundamentals";
import { SymbolBrief } from "../SymbolBrief";
import { SymbolOutlook } from "../SymbolOutlook";
import { SymbolForecast } from "../SymbolForecast";
import { SymbolSmartMoney } from "../SymbolSmartMoney";
import { Watchlist } from "../Watchlist";
import { WebHeadlines } from "../WebHeadlines";
import { tickerOf } from "../../lib/symbol";
import { Tabs, usePersistedTab } from "../ui/Tabs";
import { MarketStatus } from "./MarketStatus";
import { RailCloseButton } from "./RailToggle";

/**
 * The working set: what you are following, and what it is worth.
 */
export function RightRail({
  symbol,
  selected,
  onSelect,
  onHide,
}: {
  symbol: string;
  selected: string;
  onSelect: (s: string) => void;
  onHide: () => void;
}) {
  const [tab, setTab] = usePersistedTab<"watchlist" | "context">("rail.right", "watchlist");

  return (
    <div className="flex min-w-0 flex-1 flex-col divide-y divide-border-subtle bg-bg-panel">
      <MarketStatus />

      {/* The watchlist moved here from the left, where the navigation now
          lives. Instrument context keeps it company because the two are read
          together: you pick a name on one tab and ask what it is worth on the
          other. Alerts left this rail entirely — they are a destination in
          the navigation now, with room for the evidence behind each one. */}
      <Tabs
        tabs={[
          { value: "watchlist", label: "Watchlist" },
          { value: "context", label: "Context" },
        ]}
        value={tab}
        onChange={setTab}
        action={<RailCloseButton side="right" onClose={onHide} />}
      />

      {tab === "watchlist" ? (
        <Watchlist selected={selected} onSelect={onSelect} />
      ) : (
        <div className="min-h-0 flex-1 overflow-y-auto">
          <Fundamentals symbol={symbol} />
          <SymbolBrief symbol={symbol} />
          <SymbolForecast symbol={symbol} />
          <SymbolOutlook symbol={symbol} />
          <SymbolSmartMoney symbol={symbol} />
          <WebHeadlines
            query={`${tickerOf(symbol)} stock`}
            title={`${tickerOf(symbol)} on the web`}
            first={4}
            className="border-t border-border-subtle px-4 py-4"
          />
        </div>
      )}
    </div>
  );
}
