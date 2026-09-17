import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import { CommandPalette } from "./components/CommandPalette";
import { Login } from "./components/Login";
import { SideNav } from "./components/layout/SideNav";
import { RightRail } from "./components/layout/RightRail";
import { AlertsPage } from "./components/pages/AlertsPage";
import { AlgorithmsPage } from "./components/pages/AlgorithmsPage";
import { CalibrationPage } from "./components/pages/CalibrationPage";
import { ChartsPage } from "./components/pages/ChartsPage";
import { CongressPage } from "./components/pages/CongressPage";
import { DashboardPage } from "./components/pages/DashboardPage";
import { EventStudyPage } from "./components/pages/EventStudyPage";
import { GeopoliticsPage } from "./components/pages/GeopoliticsPage";
import { JournalPage } from "./components/pages/JournalPage";
import { PositionsPage } from "./components/pages/PositionsPage";
import { NewsPage } from "./components/pages/NewsPage";
import { ResearchPage } from "./components/pages/ResearchPage";
import { ScannerPage } from "./components/pages/ScannerPage";
import { SourcesPage } from "./components/pages/SourcesPage";
import { api } from "./lib/api";
import { setDisplayTimeZone } from "./lib/format";
import { useResize, usePersisted } from "./lib/layout";
import { StreamContext, useMarketStream } from "./lib/useStream";
import { CollapsedRail } from "./components/layout/RailToggle";
import { Divider } from "./components/ui/Divider";

/**
 * The shell.
 *
 * A fixed-height flex layout with no document scroll: every pane owns its own
 * scroll region, so the top bar and the rails never leave the viewport and a
 * long news feed cannot push the chart off the screen.
 *
 * The rails carry their own border on the side facing the centre, which is why
 * neither the rails nor the main column have outer padding — padding there
 * would break the continuous 1px seam that holds the layout together.
 */
export function App() {
  // Authentication gates the whole shell rather than individual routes: with
  // one account there is nothing to show a signed-out visitor, and rendering
  // the layout behind a modal would leak the watchlist through the DOM.
  const auth = useQuery({
    queryKey: ["auth-status"],
    queryFn: api.authStatus,
    retry: false,
    staleTime: 60_000,
  });
  const qc = useQueryClient();

  const stream = useMarketStream();
  const [selected, setSelected] = useState("AAPL");
  const [palette, setPalette] = useState(false);

  // Rail geometry, remembered. Density is a matter of screen and of taste, so
  // the widths are the operator's to set rather than something to guess at.
  const [rightShut, setRightShut] = usePersisted("rail.right.shut", false);
  const right = useResize({ key: "rail.right.width", initial: 356, min: 260, max: 560, direction: "w" });
  const location = useLocation();

  // Every timestamp in the interface is rendered in the operator's zone, which
  // the server owns. Set once rather than threaded through every component.
  const { data: meta } = useQuery({ queryKey: ["meta"], queryFn: api.meta, staleTime: Infinity });
  useEffect(() => {
    if (meta?.display_tz) setDisplayTimeZone(meta.display_tz);
  }, [meta?.display_tz]);

  /*
   * The working-set rail belongs on the two pages that are about one
   * instrument.
   *
   * It used to follow the Scanner and News pages too, where it showed a
   * valuation panel for whichever instrument happened to be selected —
   * fourteen ratios about RELIANCE while the operator read a scan of 750
   * other things. Six hundred pixels of an unrelated answer is not context,
   * it is noise, and those pages are better at full width.
   *
   * Navigation is not subject to this: it is on every page, on the left,
   * where an application's navigation belongs.
   */
  const railed = ["/dashboard", "/charts"].some((p) => location.pathname.startsWith(p));

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPalette((v) => !v);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  if (auth.isLoading) {
    // A blank field rather than a spinner: this resolves in milliseconds and a
    // flash of loading chrome is more noticeable than the wait.
    return <div className="h-screen bg-bg-base" />;
  }

  if (auth.data?.required && !auth.data.authenticated) {
    return <Login onSignedIn={() => qc.invalidateQueries()} />;
  }

  return (
    <StreamContext.Provider value={stream}>
      <div className="flex h-screen flex-col overflow-hidden bg-bg-base">
        <div className="flex min-h-0 flex-1">
          <SideNav stream={stream} onCommand={() => setPalette(true)} />

          {/* min-h-0 is load-bearing, not defensive.
              A flex item defaults to min-height:auto, which refuses to shrink
              below its content. Without this a page whose content is taller
              than the viewport grows past the bottom of a body that has
              overflow:hidden, so its inner scroll region is sized to the
              content, never overflows, and nothing on the page scrolls at
              all — the content is simply unreachable. */}
          <main className="flex min-h-0 min-w-0 flex-1 flex-col">
            <Routes>
              <Route path="/" element={<Navigate to="/dashboard" replace />} />
              <Route path="/dashboard" element={<DashboardPage symbol={selected} />} />
              <Route
                path="/charts"
                element={<ChartsPage symbol={selected} onSelect={setSelected} />}
              />
              {/* The second level of the navigation is a real route rather
                  than a tab inside the page above it, so it can be linked,
                  reloaded and bookmarked. Each page still owns the state its
                  two modes share — a rule being edited survives the move to
                  the backtester because one component renders both. */}
              <Route path="/scanner" element={<Navigate to="/scanner/signals" replace />} />
              <Route path="/scanner/:mode" element={<ScannerPage onSelect={setSelected} />} />
              <Route path="/news" element={<NewsPage onSelect={setSelected} />} />
              <Route path="/geopolitics" element={<GeopoliticsPage onSelect={setSelected} />} />
              <Route path="/congress" element={<CongressPage onSelect={setSelected} />} />
              <Route path="/eventstudy" element={<EventStudyPage />} />
              <Route path="/positions" element={<PositionsPage onSelect={setSelected} />} />
              <Route path="/journal" element={<JournalPage onSelect={setSelected} />} />
              <Route path="/research" element={<ResearchPage />} />
              <Route path="/algorithms" element={<Navigate to="/algorithms/build" replace />} />
              <Route path="/algorithms/:mode" element={<AlgorithmsPage />} />
              <Route path="/alerts" element={<AlertsPage onSelect={setSelected} />} />
              <Route path="/sources" element={<SourcesPage />} />
              <Route path="/ai" element={<CalibrationPage />} />
              <Route path="*" element={<Navigate to="/dashboard" replace />} />
            </Routes>
          </main>

          {railed &&
            (rightShut ? (
              <CollapsedRail side="right" label="Watchlist" onOpen={() => setRightShut(false)} />
            ) : (
              <>
                <Divider resize={right} orientation="vertical" />
                <div className="flex shrink-0" style={{ width: right.size }}>
                  <RightRail
                    symbol={selected}
                    selected={selected}
                    onSelect={setSelected}
                    onHide={() => setRightShut(true)}
                  />
                </div>
              </>
            ))}
        </div>

        <CommandPalette
          open={palette}
          onClose={() => setPalette(false)}
          onSelect={setSelected}
        />
      </div>
    </StreamContext.Provider>
  );
}
