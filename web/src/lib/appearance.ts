export const STYLES = ["continuum", "shadcn"] as const;

/** Style is the palette family; it is independent of the light/dark theme. */
export type Style = (typeof STYLES)[number];

export const STYLE_LABEL: Record<Style, string> = {
  continuum: "Continuum",
  shadcn: "shadcn/ui",
};

const KEY = "style";

export function parseStyle(v: string | null): Style {
  return STYLES.find((s) => s === v) ?? "continuum";
}

// Storage can be blocked, so both directions swallow its errors; the choice
// then lasts for the page only. index.html reads the same key before first
// paint, so a change of key or values must change both.
export function loadStyle(): Style {
  try {
    return parseStyle(localStorage.getItem(KEY));
  } catch {
    return "continuum";
  }
}

export function saveStyle(s: Style): void {
  try {
    localStorage.setItem(KEY, s);
  } catch {
    // see above.
  }
}
