import { useQuery, useQueryClient } from "@tanstack/react-query";
import { lazy, Suspense, useEffect, useState } from "react";
import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import { CommandPalette } from "./components/CommandPalette";
import { Login } from "./components/Login";
import { SideNav } from "./components/layout/SideNav";
import { RightRail } from "./components/layout/RightRail";
import { OverviewPage } from "./components/pages/OverviewPage";
import { api } from "./lib/api";
import { setDisplayTimeZone } from "./lib/format";
import { useMedia, useResize, usePersisted } from "./lib/layout";
import { StreamContext, useMarketStream } from "./lib/useStream";
import { loaders, preload } from "./lib/routes";
import { MobileTabBar, MobileTopBar } from "./components/layout/MobileNav";
import { titleFor } from "./components/layout/nav";
import { CollapsedRail } from "./components/layout/RailToggle";
import { Divider } from "./components/ui/Divider";

const page = <K extends keyof typeof loaders, N extends string>(key: K, name: N) =>
  lazy(() => loaders[key]().then((m) => ({ default: (m as unknown as Record<N, React.ComponentType<any>>)[name] })));
const AlertsPage = page("/alerts", "AlertsPage");
const AlgorithmsPage = page("/algorithms", "AlgorithmsPage");
const CalendarPage = page("/calendar", "CalendarPage");
const CalibrationPage = page("/ai", "CalibrationPage");
const ChartsPage = page("/charts", "ChartsPage");
const CongressPage = page("/congress", "CongressPage");
const EventStudyPage = page("/eventstudy", "EventStudyPage");
const GeopoliticsPage = page("/geopolitics", "GeopoliticsPage");
const JournalPage = page("/journal", "JournalPage");
const PositionsPage = page("/positions", "PositionsPage");
const NewsPage = page("/news", "NewsPage");
const ResearchPage = page("/research", "ResearchPage");
const ScannerPage = page("/scanner", "ScannerPage");
const SourcesPage = page("/sources", "SourcesPage");

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
  const right = useResize({
    key: "rail.right.width",
    initial: 356,
    min: 260,
    max: 560,
    direction: "w",
  });
  const location = useLocation();
  const phone = useMedia("(max-width: 767px)");
  const wide = useMedia("(min-width: 1280px)");

  useEffect(() => {
    const t = titleFor(location.pathname);
    document.title = t === "Bellwether" ? t : `${t} · Bellwether`;
  }, [location.pathname]);

  // Warm the pages a day is spent on once the first one has painted.
  useEffect(() => {
    const idle = (window as any).requestIdleCallback ?? ((f: () => void) => setTimeout(f, 1200));
    idle(() => ["/news", "/charts", "/scanner", "/research"].forEach(preload));
  }, []);

  // Every timestamp in the interface is rendered in the operator's zone, which
  // the server owns. Set once rather than threaded through every component.
  const { data: meta } = useQuery({
    queryKey: ["meta"],
    queryFn: api.meta,
    staleTime: Infinity,
  });
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
  const railed = ["/charts"].some((p) =>
    location.pathname.startsWith(p),
  );

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
    return <div className="h-[100dvh] bg-bg-base" />;
  }

  if (auth.isError) {
    return (
      <div className="flex h-[100dvh] items-center justify-center bg-bg-base px-6">
        <div className="max-w-sm text-ui text-text-secondary">
          <p className="font-reading text-display text-text-primary">Bellwether can’t reach its server.</p>
          <p className="mt-2">Check that the app is running, then try again.</p>
          <button type="button" onClick={() => auth.refetch()} className="action-primary mt-5">
            Try again
          </button>
        </div>
      </div>
    );
  }

  if (auth.data?.required && !auth.data.authenticated) {
    return (
      <Login
        setupRequired={auth.data.setup_required}
        onSignedIn={() => qc.invalidateQueries()}
      />
    );
  }

  return (
    <StreamContext.Provider value={stream}>
      <div className="flex h-[100dvh] flex-col overflow-hidden bg-bg-base">
        {phone && <MobileTopBar onCommand={() => setPalette(true)} />}
        <div className="flex min-h-0 flex-1">
          {!phone && <SideNav stream={stream} onCommand={() => setPalette(true)} defaultShut={!wide} />}

          {/* min-h-0 is load-bearing, not defensive.
              A flex item defaults to min-height:auto, which refuses to shrink
              below its content. Without this a page whose content is taller
              than the viewport grows past the bottom of a body that has
              overflow:hidden, so its inner scroll region is sized to the
              content, never overflows, and nothing on the page scrolls at
              all — the content is simply unreachable. */}
          <main className="flex min-h-0 min-w-0 flex-1 flex-col">
            <Suspense fallback={<PageLoading />}>
            <Routes>
              <Route path="/" element={<Navigate to="/dashboard" replace />} />
              <Route
                path="/dashboard"
                element={<OverviewPage onSelect={setSelected} />}
              />
              <Route
                path="/charts"
                element={
                  <ChartsPage symbol={selected} onSelect={setSelected} />
                }
              />
              {/* The second level of the navigation is a real route rather
                  than a tab inside the page above it, so it can be linked,
                  reloaded and bookmarked. Each page still owns the state its
                  two modes share — a rule being edited survives the move to
                  the backtester because one component renders both. */}
              <Route
                path="/scanner"
                element={<Navigate to="/scanner/signals" replace />}
              />
              <Route
                path="/scanner/:mode"
                element={<ScannerPage onSelect={setSelected} />}
              />
              <Route
                path="/news"
                element={<NewsPage onSelect={setSelected} />}
              />
              <Route
                path="/geopolitics"
                element={<GeopoliticsPage onSelect={setSelected} />}
              />
              <Route
                path="/congress"
                element={<CongressPage onSelect={setSelected} />}
              />
              <Route path="/eventstudy" element={<EventStudyPage />} />
              <Route
                path="/calendar"
                element={<CalendarPage onSelect={setSelected} />}
              />
              <Route
                path="/positions"
                element={<PositionsPage onSelect={setSelected} />}
              />
              <Route
                path="/journal"
                element={<JournalPage onSelect={setSelected} />}
              />
              <Route path="/research" element={<ResearchPage />} />
              <Route
                path="/algorithms"
                element={<Navigate to="/algorithms/build" replace />}
              />
              <Route path="/algorithms/:mode" element={<AlgorithmsPage />} />
              <Route
                path="/alerts"
                element={<AlertsPage onSelect={setSelected} />}
              />
              <Route path="/sources" element={<SourcesPage />} />
              <Route path="/ai" element={<CalibrationPage />} />
              <Route path="*" element={<Navigate to="/dashboard" replace />} />
            </Routes>
            </Suspense>
          </main>

          {railed && !phone &&
            (rightShut ? (
              <CollapsedRail
                side="right"
                label="Watchlist"
                onOpen={() => setRightShut(false)}
              />
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

        {phone && <MobileTabBar stream={stream} />}

        <CommandPalette
          open={palette}
          onClose={() => setPalette(false)}
          onSelect={setSelected}
        />
      </div>
    </StreamContext.Provider>
  );
}

/** A page's code is loading: the page's shape, quietly, rather than a word. */
function PageLoading() {
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4 bg-bg-panel px-6 py-7 [animation:fade-in_300ms_ease_150ms_both]">
      <span className="skeleton h-7 w-48" />
      <span className="skeleton h-4 w-80 max-w-full" />
      <div className="mt-4 flex flex-col gap-3">
        {Array.from({ length: 6 }, (_, i) => (
          <span key={i} className="skeleton h-12 w-full" />
        ))}
      </div>
    </div>
  );
}
