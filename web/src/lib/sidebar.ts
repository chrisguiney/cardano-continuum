const KEY = "sidebar";

// Storage can be blocked, so both directions swallow its errors and the
// choice then lasts for the page only. The app mounts after the script that
// reads this, so the first render already has the stored width.
export function loadCollapsed(): boolean {
  try {
    return localStorage.getItem(KEY) === "collapsed";
  } catch {
    return false;
  }
}

export function saveCollapsed(c: boolean): void {
  try {
    localStorage.setItem(KEY, c ? "collapsed" : "expanded");
  } catch {
    // see above.
  }
}
