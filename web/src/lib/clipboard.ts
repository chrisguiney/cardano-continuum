export interface ClipboardEnv {
  clipboard?: { writeText(text: string): Promise<void> };
  /** exec is the legacy synchronous copy; it reports whether the browser accepted it. */
  exec: (text: string) => boolean;
}

/** copyText puts text on the clipboard, falling back to the legacy path where the async API is missing or refuses (an insecure origin, a denied permission). */
export async function copyText(text: string, env: ClipboardEnv): Promise<boolean> {
  if (env.clipboard) {
    try {
      await env.clipboard.writeText(text);
      return true;
    } catch {
      // fall through to the legacy copy.
    }
  }
  return env.exec(text);
}

/** browserEnv is the real clipboard of the page. */
export function browserEnv(): ClipboardEnv {
  return {
    clipboard: typeof navigator !== "undefined" ? navigator.clipboard : undefined,
    exec(text) {
      const ta = document.createElement("textarea");
      ta.value = text;
      ta.setAttribute("readonly", "");
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      try {
        return document.execCommand("copy");
      } catch {
        return false;
      } finally {
        ta.remove();
      }
    },
  };
}
