import { afterEach } from "vitest";

// Tests that render into a DOM opt in with `// @vitest-environment jsdom`;
// the rest render to a string under node and need none of this.
if (typeof window !== "undefined") {
  const { cleanup } = await import("@testing-library/react");
  afterEach(cleanup);

  // Radix measures its popovers; jsdom has no layout, so a no-op observer will do.
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
}
