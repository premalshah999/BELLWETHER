import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { App } from "./App";
import { ApiError } from "./lib/api";
import "./index.css";

/**
 * A session that expires while the app is open must surface, not fail
 * silently.
 *
 * Without this every query starts returning 401, the panels keep their last
 * rendered data, and the interface looks alive while nothing in it is
 * current — the same class of failure as a badge reading LIVE over a frozen
 * screen. Re-checking auth on the first 401 flips the shell to the login
 * form instead.
 */
function onUnauthorized(error: unknown) {
  if (error instanceof ApiError && error.status === 401) {
    queryClient.invalidateQueries({ queryKey: ["auth-status"] });
  }
}

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // Live data arrives on the stream, and the queries below are the
      // reconciliation path rather than the delivery mechanism. Refetching on
      // every window focus would spend provider budget re-asking for what the
      // stream has already delivered.
      refetchOnWindowFocus: false,
      staleTime: 30_000,
      // Mutations report through the block above; queries report here.
      // eslint-disable-next-line @typescript-eslint/no-unused-vars
      retry: (failureCount, error) => {
        onUnauthorized(error);
        // Never retry a request the server has rejected on its merits — a bad
        // symbol stays bad, and retrying an auth failure just locks people out.
        if (error instanceof ApiError && error.status >= 400 && error.status < 500) {
          return false;
        }
        return failureCount < 2;
      },
      retryDelay: (attempt) => Math.min(1000 * 2 ** attempt, 8000),
    },
    mutations: { onError: onUnauthorized },
  },
});

const rootEl = document.getElementById("root");
if (!rootEl) {
  throw new Error("index.html is missing its #root element");
}

createRoot(rootEl).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
);
