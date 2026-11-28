import { useState, type ComponentProps, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { TableCell, TableRow } from "@/components/ui/table";
import { browserEnv, copyText } from "../lib/clipboard";
import { navigate } from "../lib/router";
import { cn } from "../lib/utils";
import { HelpTip } from "./fields";

export function Link({ to, children, className }: { to: string; children: ReactNode; className?: string }) {
  return (
    <a
      href={to}
      className={cn("text-primary underline underline-offset-2", className)}
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

/** Title is a view's h1. */
export function Title({ className, ...props }: ComponentProps<"h1">) {
  return <h1 className={cn("my-4 text-2xl font-bold", className)} {...props} />;
}

/** Heading is a section's h2. */
export function Heading({ className, ...props }: ComponentProps<"h2">) {
  return <h2 className={cn("mb-2 text-lg font-bold", className)} {...props} />;
}

/** Subheading is an h3 inside a section. */
export function Subheading({ className, ...props }: ComponentProps<"h3">) {
  return <h3 className={cn("mt-3 mb-2 text-base font-bold", className)} {...props} />;
}

/** Muted is secondary text: a note, a unit or an absent value. */
export function Muted({ className, ...props }: ComponentProps<"span">) {
  return <span className={cn("text-muted-foreground", className)} {...props} />;
}

/** Note is a paragraph of secondary text. */
export function Note({ className, ...props }: ComponentProps<"p">) {
  return <p className={cn("my-2 text-muted-foreground", className)} {...props} />;
}

/** Mono is an identifier: a hash, a key or an address. */
export function Mono({ className, ...props }: ComponentProps<"span">) {
  return <span className={cn("font-mono text-[0.85em]", className)} {...props} />;
}

/** Panel is a card section, the page's unit of grouping. */
export function Panel({ className, ...props }: ComponentProps<"section">) {
  return <section className={cn("my-4 min-w-0 rounded-lg border bg-card p-4 text-card-foreground", className)} {...props} />;
}

/** Grid lays panels out two or more abreast, down to one column on a narrow screen. */
export function Grid({ className, ...props }: ComponentProps<"div">) {
  return <div className={cn("grid grid-cols-[repeat(auto-fit,minmax(min(320px,100%),1fr))] gap-4 [&>section]:my-0", className)} {...props} />;
}

/** TableFrame borders a shadcn Table; the table inside scrolls sideways rather than widening the page. */
export function TableFrame({ className, ...props }: ComponentProps<"div">) {
  return <div className={cn("my-2 min-w-0 overflow-hidden rounded-lg border bg-card", className)} {...props} />;
}

/** EmptyRow is the one row of a table with nothing to list. */
export function EmptyRow({ cols, children }: { cols: number; children: ReactNode }) {
  return (
    <TableRow className="hover:bg-transparent">
      <TableCell colSpan={cols} className="py-4 text-center whitespace-normal text-muted-foreground">{children}</TableCell>
    </TableRow>
  );
}

/** Filters is the row of filter controls above a list. */
export function Filters({ className, ...props }: ComponentProps<"form">) {
  return <form className={cn("mb-4 flex flex-wrap items-end gap-4", className)} onSubmit={(e) => e.preventDefault()} {...props} />;
}

/** Banner is a one-line notice: a fork, a dropped stream, a control's result. */
export function Banner({ tone, className, ...props }: ComponentProps<"div"> & { tone: "ok" | "warn" | "bad" }) {
  const color = { ok: "text-ok", warn: "text-warn", bad: "text-destructive" }[tone];
  return <div className={cn("my-2 rounded-md border border-current bg-card px-3 py-2 font-semibold [&_p]:my-0", color, className)} {...props} />;
}

/** Facts is a two-column list of names and values that stacks on a narrow screen. */
export function Facts({ rows, className }: { rows: [string, ReactNode][]; className?: string }) {
  return (
    <dl className={cn("grid grid-cols-1 gap-x-5 gap-y-1.5 sm:grid-cols-[max-content_1fr]", className)}>
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="font-semibold">{k}</dt>
          <dd className="mb-2 min-w-0 [overflow-wrap:anywhere] sm:mb-0">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

export function ErrorNote({ error }: { error: string | null }) {
  return error ? (
    <p className="my-2 font-semibold text-destructive" role="alert">
      Could not load: {error}
    </p>
  ) : null;
}

export function Stat({ label, value, sub, help }: { label: string; value: string; sub?: string; help?: string }) {
  return (
    <div className="rounded-lg border bg-card px-4 py-3">
      <div className="flex items-center gap-1.5 text-xs tracking-wider text-muted-foreground uppercase">
        {label}
        {help && <HelpTip id={`stat-${label.replace(/\W+/g, "-")}`} label={label} text={help} />}
      </div>
      <div className="text-2xl font-semibold">{value}</div>
      {sub && <div className="text-xs [overflow-wrap:anywhere] text-muted-foreground">{sub}</div>}
    </div>
  );
}

/** Stats is the row of headline numbers at the top of a view. */
export function Stats({ className, ...props }: ComponentProps<"div">) {
  return <div className={cn("grid grid-cols-[repeat(auto-fit,minmax(170px,1fr))] gap-3", className)} {...props} />;
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
    <Button type="button" variant="ghost" size="xs" className="ml-1 h-5 px-1.5 text-xs font-normal" onClick={click} aria-label={`Copy ${label}`}>
      {state === "ok" ? "Copied" : state === "fail" ? "Copy failed" : "Copy"}
    </Button>
  );
}
