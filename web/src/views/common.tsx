import type { ReactNode } from "react";
import { useState } from "react";
import { browserEnv, copyText } from "../lib/clipboard";
import { navigate } from "../lib/router";
import { HelpTip } from "./fields";

export function Link({ to, children }: { to: string; children: ReactNode }) {
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
    <p className="error" role="alert">
      Could not load: {error}
    </p>
  ) : null;
}

export function Stat({ label, value, sub, help }: { label: string; value: string; sub?: string; help?: string }) {
  return (
    <div className="stat">
      <div className="stat-label">{label}{help && <HelpTip id={`stat-${label.replace(/\W+/g, "-")}`} label={label} text={help} />}</div>
      <div className="stat-value">{value}</div>
      {sub && <div className="stat-sub">{sub}</div>}
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
    <button type="button" className="copy ghost" onClick={click} aria-label={`Copy ${label}`}>
      {state === "ok" ? "Copied" : state === "fail" ? "Copy failed" : "Copy"}
    </button>
  );
}
