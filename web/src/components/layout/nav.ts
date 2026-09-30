import {
  Bell,
  BookOpen,
  BrainCircuit,
  Briefcase,
  CalendarClock,
  CandlestickChart,
  Filter,
  FlaskConical,
  Landmark,
  Newspaper,
  Radar,
  Rss,
  ScrollText,
  Search,
  SlidersHorizontal,
  Sunrise,
  TestTubeDiagonal,
  type LucideIcon,
} from "lucide-react";

export interface NavItem {
  to: string;
  label: string;
  icon: LucideIcon;
  /** Named so a badge can be attached without the nav knowing what it counts. */
  badge?: "alerts";
  /** Also active on these path prefixes. */
  match?: string[];
}

export interface NavSection {
  label?: string;
  items: NavItem[];
}

/**
 * Where everything lives.
 *
 * The first five are what a day is spent on, so they are never behind a
 * heading. Everything else is grouped by the job it does, and every group is
 * always open: a menu you have to expand is a menu whose contents you have to
 * remember.
 */
export const NAV: NavSection[] = [
  {
    items: [
      { to: "/dashboard", label: "Today", icon: Sunrise },
      { to: "/news", label: "News", icon: Newspaper },
      { to: "/charts", label: "Charts", icon: CandlestickChart },
      { to: "/scanner/signals", label: "Signals", icon: Radar },
      { to: "/research", label: "Research", icon: Search },
    ],
  },
  {
    label: "Markets",
    items: [
      { to: "/calendar", label: "Catalysts", icon: CalendarClock },
      { to: "/geopolitics", label: "Policy & macro", icon: ScrollText },
      { to: "/congress", label: "Congress trades", icon: Landmark },
      { to: "/scanner/screens", label: "Screens", icon: Filter },
    ],
  },
  {
    label: "Portfolio",
    items: [
      { to: "/positions", label: "Positions", icon: Briefcase },
      { to: "/journal", label: "Journal", icon: BookOpen },
    ],
  },
  {
    label: "Automate",
    items: [
      { to: "/alerts", label: "Alerts", icon: Bell, badge: "alerts" },
      { to: "/algorithms/build", label: "Algorithms", icon: SlidersHorizontal },
      { to: "/algorithms/backtest", label: "Backtest", icon: FlaskConical },
    ],
  },
  {
    label: "Evidence",
    items: [
      { to: "/eventstudy", label: "Event study", icon: TestTubeDiagonal },
      { to: "/sources", label: "Data sources", icon: Rss },
      { to: "/ai", label: "AI track record", icon: BrainCircuit },
    ],
  },
];

/** The phone's tab bar: the daily five, with everything else under More. */
export const TAB_BAR: NavItem[] = [
  { to: "/dashboard", label: "Today", icon: Sunrise },
  { to: "/news", label: "News", icon: Newspaper },
  { to: "/charts", label: "Charts", icon: CandlestickChart },
  { to: "/research", label: "Research", icon: Search },
];

export const ALL_ITEMS = NAV.flatMap((s) => s.items);

/** The page's own name, for the phone header and the document title. */
export function titleFor(path: string): string {
  const hit = ALL_ITEMS.find((i) => path.startsWith(i.to) || i.match?.some((m) => path.startsWith(m)));
  return hit?.label ?? "Bellwether";
}
