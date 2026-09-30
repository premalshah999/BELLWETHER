import { useQuery } from "@tanstack/react-query";
import { PanelLeftClose, PanelLeftOpen } from "lucide-react";
import { NavLink, useLocation } from "react-router-dom";
import { api } from "../../lib/api";
import { usePersisted } from "../../lib/layout";
import { preload } from "../../lib/routes";
import type { StreamState } from "../../lib/stream";
import { BrandMark, NavFooter, SearchButton } from "./NavChrome";
import { NAV, type NavItem } from "./nav";

export function useUnreadAlerts() {
  const { data } = useQuery({
    queryKey: ["alerts"],
    queryFn: () => api.alerts({ limit: 40 }),
    refetchInterval: 30_000,
  });
  return data?.unread ?? 0;
}

export function isActive(item: NavItem, path: string) {
  return path.startsWith(item.to) || !!item.match?.some((m) => path.startsWith(m));
}

/**
 * The navigation, for screens wide enough to keep it open.
 *
 * Collapsed, it becomes a rail of icons with the labels as tooltips; the
 * choice is remembered because it is a matter of screen and taste.
 */
export function SideNav({
  stream,
  onCommand,
  defaultShut,
}: {
  stream: StreamState;
  onCommand: () => void;
  defaultShut: boolean;
}) {
  const [shutPref, setShut] = usePersisted<boolean | null>("nav.collapsed", null);
  const shut = shutPref ?? defaultShut;
  const location = useLocation();
  const unread = useUnreadAlerts();

  return (
    <nav
      aria-label="Main"
      className={
        "flex shrink-0 flex-col border-r border-border-subtle bg-bg-panel transition-[width] duration-200 " +
        (shut ? "w-16" : "w-60")
      }
    >
      <div className={"flex h-14 shrink-0 items-center gap-2.5 " + (shut ? "justify-center" : "px-4")}>
        <BrandMark />
        {!shut && (
          <span className="font-reading min-w-0 flex-1 truncate text-[17px] font-semibold text-text-primary">
            Bellwether
          </span>
        )}
        {!shut && (
          <button
            type="button"
            onClick={() => setShut(true)}
            aria-label="Collapse navigation"
            title="Collapse navigation"
            className="flex h-7 w-7 items-center justify-center rounded-md text-text-muted transition-colors hover:bg-bg-panel-hover hover:text-text-primary"
          >
            <PanelLeftClose size={15} />
          </button>
        )}
      </div>

      <div className={shut ? "flex justify-center pb-2" : "px-3 pb-2"}>
        <SearchButton onClick={onCommand} compact={shut} />
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto overflow-x-hidden pb-3">
        {NAV.map((section, i) => (
          <div key={section.label ?? i} className={i > 0 ? "mt-3" : "mt-1"}>
            {section.label &&
              (shut ? (
                <div className="mx-4 mb-2 border-t border-border-subtle" />
              ) : (
                <p className="px-5 pb-1 text-meta font-medium text-text-muted">{section.label}</p>
              ))}
            <ul className="flex flex-col gap-px px-2">
              {section.items.map((item) => (
                <li key={item.to}>
                  <NavRow item={item} shut={shut} unread={unread} active={isActive(item, location.pathname)} />
                </li>
              ))}
            </ul>
          </div>
        ))}
      </div>

      {shut && (
        <div className="flex justify-center pb-1">
          <button
            type="button"
            onClick={() => setShut(false)}
            aria-label="Expand navigation"
            title="Expand navigation"
            className="flex h-8 w-8 items-center justify-center rounded-md text-text-muted transition-colors hover:bg-bg-panel-hover hover:text-text-primary"
          >
            <PanelLeftOpen size={15} />
          </button>
        </div>
      )}
      <NavFooter shut={shut} stream={stream} />
    </nav>
  );
}

function NavRow({
  item,
  shut,
  unread,
  active,
}: {
  item: NavItem;
  shut: boolean;
  unread: number;
  active: boolean;
}) {
  const count = item.badge === "alerts" && unread > 0 ? unread : 0;
  return (
    <NavLink
      to={item.to}
      title={shut ? item.label : undefined}
      onMouseEnter={() => preload(item.to)}
      onFocus={() => preload(item.to)}
      aria-current={active ? "page" : undefined}
      className={
        "group relative flex h-9 items-center gap-3 rounded-md text-ui transition-colors " +
        (shut ? "justify-center " : "px-3 ") +
        (active
          ? "bg-brand-muted font-medium text-text-primary"
          : "text-text-secondary hover:bg-bg-panel-hover hover:text-text-primary")
      }
    >
      <item.icon
        size={17}
        strokeWidth={active ? 2.1 : 1.8}
        className={"shrink-0 " + (active ? "text-brand" : "text-text-muted group-hover:text-text-secondary")}
      />
      {!shut && <span className="min-w-0 flex-1 truncate">{item.label}</span>}
      {count > 0 &&
        (shut ? (
          <span className="absolute right-2.5 top-2 h-2 w-2 rounded-full bg-brand ring-2 ring-bg-panel" />
        ) : (
          <span className="shrink-0 rounded-full bg-brand px-1.5 text-micro font-semibold leading-[18px] text-brand-ink">
            {count > 99 ? "99+" : count}
          </span>
        ))}
    </NavLink>
  );
}
