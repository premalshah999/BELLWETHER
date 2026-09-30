import { useMutation } from "@tanstack/react-query";
import { KeyRound } from "lucide-react";
import { useState } from "react";
import { api } from "../lib/api";
import { BrandMark } from "./layout/NavChrome";

/**
 * The sign-in screen: one field, and what to do if you do not have the key.
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
    <div className="flex h-[100dvh] items-center justify-center bg-bg-base px-5">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (key.trim()) login.mutate(key.trim());
        }}
        className="w-full max-w-sm [animation:rise-in_280ms_var(--ease-out)]"
      >
        <div className="flex items-center gap-3">
          <BrandMark size={34} />
          <span className="font-reading text-[26px] font-semibold text-text-primary">Bellwether</span>
        </div>
        <h1 className="mt-8 text-display font-semibold text-text-primary">Sign in</h1>
        <p className="mt-1 text-ui text-text-secondary">Use the access key issued to you on the server.</p>

        {setupRequired && (
          <p role="status" className="mt-4 rounded-md bg-brand-muted px-3 py-2.5 text-ui text-text-primary">
            This workspace has no access keys yet. Ask the server owner to run{" "}
            <code className="text-meta">tradesys -issue-key</code> first.
          </p>
        )}

        <label htmlFor="access-key" className="mt-6 block text-meta font-medium text-text-secondary">
          Access key
        </label>
        <div className="relative mt-1.5">
          <KeyRound size={16} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-text-muted" />
          <input
            id="access-key"
            type="password"
            autoFocus
            autoComplete="current-password"
            spellCheck={false}
            placeholder="tsk_…"
            value={key}
            onChange={(e) => setKey(e.target.value)}
            className="h-11 w-full rounded-lg border border-border-focus bg-bg-panel pl-10 pr-3 text-[16px] outline-none transition-colors focus:border-brand"
          />
        </div>

        {login.isError && (
          <p role="alert" className="mt-2 text-ui text-semantic-down">
            {(login.error as Error)?.message || "That key was not accepted. Check it and try again."}
          </p>
        )}

        <button type="submit" disabled={!key.trim() || login.isPending} className="action-primary mt-4 h-11 w-full disabled:opacity-50">
          {login.isPending ? "Signing in…" : "Sign in"}
        </button>

        <p className="mt-6 text-meta leading-relaxed text-text-muted">
          Keys can’t be recovered. If you’ve lost yours, ask an owner to issue a new one.
        </p>
      </form>
    </div>
  );
}
