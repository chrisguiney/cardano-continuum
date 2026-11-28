import { cn } from "../lib/utils";

/**
 * CodeBlock shows text in monospace with a line-number gutter. The numbers
 * are a separate column, so selecting the code does not copy them, and the
 * block scrolls in both directions rather than wrapping a line.
 */
export function CodeBlock({ text, label, className }: { text: string; label: string; className?: string }) {
  const lines = text.replace(/\n$/, "").split("\n");
  return (
    <div role="region" aria-label={label} tabIndex={0} className={cn("mt-2 flex max-h-96 overflow-auto rounded-md border bg-muted font-mono text-[0.85em]", className)}>
      <pre aria-hidden="true" className="sticky left-0 select-none border-r bg-muted px-2.5 py-2 text-right text-muted-foreground">{lines.map((_, i) => i + 1).join("\n")}</pre>
      <pre className="px-2.5 py-2">{lines.join("\n")}</pre>
    </div>
  );
}
