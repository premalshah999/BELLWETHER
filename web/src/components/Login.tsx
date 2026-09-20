import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../lib/api";

/**
 * The sign-in screen.
 *
 * Deliberately plain. There is one account and one field, so there is nothing
 * to brand, explain or upsell — and a login page that looks like a product
 * page invites people to wonder what product it is.
 */
export function Login({
  onSignedIn,
  setupRequired,
}: {
  onSignedIn: () => void;
  setupRequired?: boolean;
}) {
  const [key, setKey] = useState("");
  const login = useMutation({
    mutationFn: (k: string) => api.login(k),
    onSuccess: onSignedIn,
  });

  return (
    <div className="flex h-screen items-center justify-center bg-bg-base">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (key.trim()) login.mutate(key.trim());
        }}
        className="w-80 border border-border-subtle bg-bg-panel"
      >
        <div className="flex items-center gap-2 border-b border-border-subtle px-4 py-3">
          <span className="h-3.5 w-3.5 bg-brand shadow-[0_0_8px_rgba(0,229,255,0.55)]" />
          <span className="font-mono text-ui font-semibold tracking-[0.14em] text-text-primary">
            BELLWETHER
          </span>
        </div>

        <div className="p-4">
          {setupRequired && (
            <p role="status" className="mb-4 text-ui text-text-secondary">
              This workspace is awaiting its first access key. Ask the server
              owner to issue one before signing in.
            </p>
          )}
          <label
            htmlFor="passphrase"
            className="mb-2 block font-mono text-micro uppercase tracking-[0.14em] text-text-muted"
          >
            Access key
          </label>
          <input
            id="passphrase"
            type="password"
            autoFocus
            autoComplete="current-password"
            placeholder="tsk_…"
            value={key}
            onChange={(e) => setKey(e.target.value)}
            className="w-full border border-border-subtle bg-bg-base px-2.5 py-2 font-mono text-ui outline-none focus:border-brand"
          />

          <p className="mt-1.5 text-meta leading-relaxed text-text-muted">
            Keys are issued on the server and cannot be recovered — if you have
            lost yours, ask an owner to issue another.
          </p>

          {login.isError && (
            <p className="mt-2 text-meta text-semantic-down">
              {(login.error as Error)?.message ?? "Sign in failed."}
            </p>
          )}

          <button
            type="submit"
            disabled={!key.trim() || login.isPending}
            className="mt-3 w-full border border-brand bg-brand-muted py-2 font-mono text-micro uppercase tracking-wider text-brand transition-colors hover:bg-brand hover:text-bg-base disabled:opacity-40"
          >
            {login.isPending ? "checking…" : "sign in"}
          </button>
        </div>
      </form>
    </div>
  );
}
