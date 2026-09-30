import { useQuery } from "@tanstack/react-query";
import { Globe } from "lucide-react";
import { useState } from "react";
import { api } from "../lib/api";
import { formatAgo, formatDateTime } from "../lib/format";
import { safeHref } from "../lib/url";

/**
 * Headlines from the open web for a query: what the search engines find
 * beyond the sources Bellwether follows. Links go to the publisher.
 */
export function WebHeadlines({
  query,
  kind = "news",
  title = "From the web",
  first = 5,
  className = "",
}: {
  query: string;
  kind?: "news" | "web";
  title?: string;
  /** How many to show before "Show more". */
  first?: number;
  className?: string;
}) {
  const [all, setAll] = useState(false);
  const { data, isLoading, isError } = useQuery({
    queryKey: ["web", kind, query],
    queryFn: () => api.webSearch(query, kind, 20),
    enabled: query.trim().length > 1,
    staleTime: 5 * 60_000,
    retry: false,
  });
  if (query.trim().length < 2) return null;
  const results = data?.results ?? [];
  const shown = all ? results : results.slice(0, first);
  return (
    <section className={className} aria-label={title}>
      <h3 className="flex items-center gap-1.5 text-meta font-semibold text-text-secondary">
        <Globe size={13} className="text-text-muted" /> {title}
      </h3>
      {isLoading ? (
        <div className="mt-2 flex flex-col gap-2" aria-busy="true">
          {[0, 1, 2].map((i) => (
            <span key={i} className="skeleton h-9 w-full" />
          ))}
        </div>
      ) : isError ? (
        <p className="mt-2 text-meta text-text-muted">Web search is not available right now.</p>
      ) : results.length === 0 ? (
        <p className="mt-2 text-meta text-text-muted">The web has nothing recent on this.</p>
      ) : (
        <ul className="mt-1 divide-y divide-border-subtle">
          {shown.map((r) => (
            <li key={r.url} className="py-2">
              <a
                href={safeHref(r.url)}
                target="_blank"
                rel="noopener noreferrer"
                className="text-[13.5px] font-medium leading-snug text-text-primary hover:text-accent-text hover:underline"
              >
                {r.title}
              </a>
              <p className="mt-0.5 text-micro text-text-muted">
                {r.publisher}
                {r.published_at && (
                  <>
                    {" · "}
                    <time dateTime={r.published_at} title={formatAgo(r.published_at)}>
                      {formatDateTime(r.published_at)}
                    </time>
                  </>
                )}
              </p>
            </li>
          ))}
        </ul>
      )}
      {results.length > first && (
        <button type="button" onClick={() => setAll((v) => !v)} className="mt-1 text-micro font-medium text-accent-text hover:underline">
          {all ? "Show fewer" : `Show ${results.length - first} more`}
        </button>
      )}
    </section>
  );
}
