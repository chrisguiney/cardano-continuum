import type { ComponentChildren } from "preact";
import { useState } from "preact/hooks";
import { browserEnv, copyText } from "../lib/clipboard";
import { navigate } from "../lib/router";

export function Link({ to, children }: { to: string; children: ComponentChildren }) {
  return (
    <a
      href={to}
      onClick={(e) => {
        if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
        e.preventDefault();
        navigate(to);
      }}
    >
      {children}
    </a>
  );
}

export function ErrorNote({ error }: { error: string | null }) {
  return error ? (
    <p class="error" role="alert">
      Could not load: {error}
    </p>
  ) : null;
}

export function Stat({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div class="stat">
      <div class="stat-label">{label}</div>
      <div class="stat-value">{value}</div>
      {sub && <div class="stat-sub">{sub}</div>}
    </div>
  );
}

/** CopyButton copies value and says so; it falls back where the async clipboard is unavailable. */
export function CopyButton({ value, label }: { value: string; label: string }) {
  const [state, setState] = useState<"idle" | "ok" | "fail">("idle");
  const click = () => {
    void copyText(value, browserEnv()).then((ok) => {
      setState(ok ? "ok" : "fail");
      setTimeout(() => setState("idle"), 1500);
    });
  };
  return (
    <button type="button" class="copy ghost" onClick={click} aria-label={`Copy ${label}`}>
      {state === "ok" ? "Copied" : state === "fail" ? "Copy failed" : "Copy"}
    </button>
  );
}
