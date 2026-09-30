import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Ellipsis, LogOut, X } from "lucide-react";
import { useEffect, useState } from "react";
import { NavLink, useLocation } from "react-router-dom";
import { api } from "../../lib/api";
import type { StreamState } from "../../lib/stream";
import { BrandMark, SearchButton, StatusDot, ThemeSwitch, ZoneSwitch, useSystemStatus } from "./NavChrome";
import { NAV, TAB_BAR, titleFor } from "./nav";
import { isActive, useUnreadAlerts } from "./SideNav";

/** The phone's header: where you are, and search. */
export function MobileTopBar({ onCommand }: { onCommand: () => void }) {
  const { pathname } = useLocation();
  return (
    <header className="flex h-13 shrink-0 items-center gap-3 border-b border-border-subtle bg-bg-panel px-4 pt-[env(safe-area-inset-top)]">
      <BrandMark size={24} />
      <h1 className="min-w-0 flex-1 truncate text-[17px] font-semibold tracking-[-0.01em] text-text-primary">
        {titleFor(pathname)}
      </h1>
      <SearchButton onClick={onCommand} compact />
    </header>
  );
}

/**
 * Four daily destinations under the thumb, and everything else one tap away.
 */
export function MobileTabBar({ stream }: { stream: StreamState }) {
  const { pathname } = useLocation();
  const [more, setMore] = useState(false);
  const unread = useUnreadAlerts();
  const inTabs = TAB_BAR.some((t) => isActive(t, pathname));

  useEffect(() => setMore(false), [pathname]);

  return (
    <>
      <nav
        aria-label="Main"
        className="grid shrink-0 grid-cols-5 border-t border-border-subtle bg-bg-panel pb-[env(safe-area-inset-bottom)]"
      >
        {TAB_BAR.map((t) => {
          const on = isActive(t, pathname);
          return (
            <NavLink
              key={t.to}
              to={t.to}
              className={
                "flex h-14 flex-col items-center justify-center gap-0.5 text-micro font-medium " +
                (on ? "text-text-primary" : "text-text-muted")
              }
            >
              <t.icon size={20} strokeWidth={on ? 2.1 : 1.8} className={on ? "text-brand" : ""} />
              {t.label}
            </NavLink>
          );
        })}
        <button
          type="button"
          onClick={() => setMore(true)}
          aria-expanded={more}
          className={
            "relative flex h-14 flex-col items-center justify-center gap-0.5 text-micro font-medium " +
            (!inTabs ? "text-text-primary" : "text-text-muted")
          }
        >
          <Ellipsis size={20} className={!inTabs ? "text-brand" : ""} />
          More
          {unread > 0 && <span className="absolute right-[30%] top-2.5 h-2 w-2 rounded-full bg-brand" />}
        </button>
      </nav>
      {more && <MoreSheet stream={stream} unread={unread} onClose={() => setMore(false)} />}
    </>
  );
}

function MoreSheet({ stream, unread, onClose }: { stream: StreamState; unread: number; onClose: () => void }) {
  const { pathname } = useLocation();
  const qc = useQueryClient();
  const s = useSystemStatus(stream);
  const { data: auth } = useQuery({ queryKey: ["auth-status"], queryFn: api.authStatus, staleTime: 60_000 });

  useEffect(() => {
    const esc = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    document.addEventListener("keydown", esc);
    return () => document.removeEventListener("keydown", esc);
  }, [onClose]);

  return (
    <div className="fixed inset-0 z-40 flex flex-col justify-end" role="dialog" aria-modal="true" aria-label="All pages">
      <button
        type="button"
        aria-label="Close"
        onClick={onClose}
        className="absolute inset-0 bg-[var(--scrim)] [animation:fade-in_160ms_ease]"
      />
      <div className="relative max-h-[85vh] overflow-y-auto overscroll-contain rounded-t-2xl bg-bg-raised pb-[env(safe-area-inset-bottom)] shadow-pop [animation:rise-in_220ms_var(--ease-out)]">
        <div className="sticky top-0 flex items-center justify-between bg-bg-raised px-5 pb-2 pt-4">
          <span className="text-[17px] font-semibold">All pages</span>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close"
            className="flex h-9 w-9 items-center justify-center rounded-full text-text-secondary hover:bg-bg-panel-hover"
          >
            <X size={18} />
          </button>
        </div>
        {NAV.map((section, i) => (
          <div key={section.label ?? i} className="px-3 pb-2">
            {section.label && <p className="px-2 pb-1 pt-2 text-meta font-medium text-text-muted">{section.label}</p>}
            <div className="grid grid-cols-2 gap-1">
              {section.items.map((item) => {
                const on = isActive(item, pathname);
                return (
                  <NavLink
                    key={item.to}
                    to={item.to}
                    className={
                      "flex h-11 items-center gap-2.5 rounded-lg px-3 text-ui " +
                      (on ? "bg-brand-muted font-medium text-text-primary" : "text-text-secondary active:bg-bg-panel-hover")
                    }
                  >
                    <item.icon size={17} className={on ? "text-brand" : "text-text-muted"} />
                    <span className="min-w-0 flex-1 truncate">{item.label}</span>
                    {item.badge === "alerts" && unread > 0 && (
                      <span className="rounded-full bg-brand px-1.5 text-micro font-semibold text-brand-ink">{unread}</span>
                    )}
                  </NavLink>
                );
              })}
            </div>
          </div>
        ))}
        <div className="mx-5 mt-2 flex items-center gap-2.5 border-t border-border-subtle py-3">
          <StatusDot tone={s.tone} pulse={s.live} />
          <span className="min-w-0 flex-1">
            <span className="block text-meta text-text-secondary">{s.summary}</span>
            <span className="block text-micro text-text-muted">{s.market}</span>
          </span>
          <ThemeSwitch />
        </div>
        <div className="mx-5 flex items-center justify-between border-t border-border-subtle py-3">
          <span className="text-meta text-text-muted">Times in</span>
          <ZoneSwitch />
        </div>
        {auth?.required && (
          <button
            type="button"
            onClick={async () => {
              await api.logout();
              qc.invalidateQueries();
            }}
            className="mx-5 mb-4 flex h-11 w-[calc(100%-2.5rem)] items-center justify-center gap-2 rounded-lg border border-border-subtle text-ui text-text-secondary"
          >
            <LogOut size={15} /> Sign out{auth.profile ? ` ${auth.profile.name}` : ""}
          </button>
        )}
      </div>
    </div>
  );
}
