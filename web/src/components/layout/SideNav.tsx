import { useQuery } from "@tanstack/react-query";
import {
  Activity,
  Bell,
  BrainCircuit,
  CandlestickChart,
  ChevronDown,
  ChevronRight,
  Filter,
  FlaskConical,
  Globe2,
  LayoutDashboard,
  Newspaper,
  Radar,
  Rss,
  Search,
  SlidersHorizontal,
} from "lucide-react";
import { NavLink, useLocation } from "react-router-dom";
import { api } from "../../lib/api";
import { usePersisted } from "../../lib/layout";
import type { StreamState } from "../../lib/stream";
import { NavFooter, NavHeader } from "./NavChrome";

type Icon = typeof LayoutDashboard;

interface Item {
  to: string;
  label: string;
  icon: Icon;
  /** Named so a badge can be attached without the nav knowing what it counts. */
  badge?: "alerts";
}

interface Group {
  id: string;
  label: string;
  icon: Icon;
  children: Item[];
}

type Entry = Item | Group;

const isGroup = (e: Entry): e is Group => "children" in e;

/**
 * The navigation.
 *
 * Flat tab strips stop working somewhere around six destinations, and this
 * app has eleven. They were being hidden in three different ways — a tab
 * inside the Scanner page, a tab inside the Algorithms page, a tab inside the
 * right rail — which meant the only way to learn that screens or backtesting
 * existed was to already know.
 *
 * Grouping makes the second level a destination in its own right rather than
 * a mode of the page above it, so every function in the app is reachable from
 * one place and visible without opening anything.
 */
const NAV: Entry[] = [
  { to: "/dashboard", label: "Dashboard", icon: LayoutDashboard },
  { to: "/charts", label: "Charts", icon: CandlestickChart },
  {
    id: "markets",
    label: "Markets",
    icon: Radar,
    children: [
      { to: "/scanner/signals", label: "Signals", icon: Radar },
      { to: "/scanner/screens", label: "Screens", icon: Filter },
      { to: "/news", label: "News", icon: Newspaper },
      { to: "/geopolitics", label: "Geopolitics", icon: Globe2 },
    ],
  },
  {
    id: "strategy",
    label: "Strategy",
    icon: SlidersHorizontal,
    children: [
      { to: "/algorithms/build", label: "Algorithms", icon: SlidersHorizontal },
      { to: "/algorithms/backtest", label: "Backtest", icon: FlaskConical },
    ],
  },
  { to: "/research", label: "Research", icon: Search },
  { to: "/alerts", label: "Alerts", icon: Bell, badge: "alerts" },
  {
    id: "system",
    label: "System",
    icon: Activity,
    children: [
      { to: "/sources", label: "Sources", icon: Rss },
      { to: "/ai", label: "AI calibration", icon: BrainCircuit },
    ],
  },
];

export function SideNav({
  stream,
  onCommand,
}: {
  stream: StreamState;
  onCommand: () => void;
}) {
  const [shut, setShut] = usePersisted("nav.shut", false);
  const location = useLocation();

  const { data: alerts } = useQuery({
    queryKey: ["alerts"],
    queryFn: () => api.alerts({ limit: 40 }),
    refetchInterval: 30_000,
  });
  const unread = alerts?.unread ?? 0;

  if (shut) {
    return (
      <nav className="flex w-12 shrink-0 flex-col items-center border-r border-border-subtle bg-bg-panel">
        <NavHeader shut onExpand={() => setShut(false)} onCommand={onCommand} />
        <div className="flex flex-1 flex-col items-center gap-1 py-2">
        {/* Collapsed, groups flatten to their children: a group header that
            cannot show its label is a button that does nothing legible. */}
        {NAV.flatMap((e) => (isGroup(e) ? e.children : [e])).map((item) => (
          <NavLink
            key={item.to}
            to={item.to}
            title={item.label}
            className={({ isActive }) =>
              "relative flex h-8 w-8 items-center justify-center transition-colors " +
              (isActive ? "bg-brand-muted text-brand" : "text-text-muted hover:text-text-primary")
            }
          >
            <item.icon size={15} />
            {item.badge === "alerts" && unread > 0 && (
              <span className="absolute right-1 top-1 h-1.5 w-1.5 rounded-full bg-brand" />
            )}
          </NavLink>
        ))}
        </div>
        <NavFooter shut stream={stream} />
      </nav>
    );
  }

  return (
    <nav className="flex w-56 shrink-0 flex-col border-r border-border-subtle bg-bg-panel">
      <NavHeader onCollapse={() => setShut(true)} onCommand={onCommand} />

      <div className="min-h-0 flex-1 overflow-y-auto py-1.5">
        {NAV.map((entry) =>
          isGroup(entry) ? (
            <NavGroup key={entry.id} group={entry} path={location.pathname} unread={unread} />
          ) : (
            <NavRow key={entry.to} item={entry} unread={unread} />
          ),
        )}
      </div>

      <NavFooter stream={stream} />
    </nav>
  );
}

function NavGroup({
  group,
  path,
  unread,
}: {
  group: Group;
  path: string;
  unread: number;
}) {
  // A group holding the current page opens itself, so arriving by any route —
  // a link, a redirect, a reload — leaves the nav showing where you are.
  const holdsCurrent = group.children.some((c) => path.startsWith(c.to));
  const [open, setOpen] = usePersisted(`nav.group.${group.id}`, true);
  const shown = open || holdsCurrent;

  return (
    <div className="mb-0.5">
      <button
        type="button"
        onClick={() => setOpen(!shown)}
        className="flex w-full items-center gap-2 px-3 py-1.5 text-left transition-colors hover:bg-bg-panel-hover"
      >
        {shown ? (
          <ChevronDown size={11} className="shrink-0 text-text-muted" />
        ) : (
          <ChevronRight size={11} className="shrink-0 text-text-muted" />
        )}
        <group.icon size={13} className="shrink-0 text-text-muted" />
        <span className="font-mono text-micro uppercase tracking-[0.12em] text-text-muted">
          {group.label}
        </span>
      </button>
      {shown &&
        group.children.map((c) => <NavRow key={c.to} item={c} unread={unread} nested />)}
    </div>
  );
}

function NavRow({
  item,
  unread,
  nested,
}: {
  item: Item;
  unread: number;
  nested?: boolean;
}) {
  return (
    <NavLink
      to={item.to}
      className={({ isActive }) =>
        "flex items-center gap-2 py-1.5 pr-3 text-ui transition-colors " +
        (nested ? "pl-8 " : "pl-3 ") +
        (isActive
          ? "bg-brand-muted text-brand"
          : "text-text-secondary hover:bg-bg-panel-hover hover:text-text-primary")
      }
    >
      {/* The active marker is a 1px edge rather than a filled block, matching
          how selection is drawn everywhere else in this interface. */}
      {({ isActive }: { isActive: boolean }) => (
        <>
          <span className={"-ml-2 h-4 w-px shrink-0 " + (isActive ? "bg-brand" : "bg-transparent")} />
          <item.icon size={13} className="shrink-0" />
          <span className="min-w-0 flex-1 truncate">{item.label}</span>
          {item.badge === "alerts" && unread > 0 && (
            <span className="shrink-0 rounded-sm bg-brand-muted px-1 py-px font-mono text-micro text-brand">
              {unread > 99 ? "99+" : unread}
            </span>
          )}
        </>
      )}
    </NavLink>
  );
}
