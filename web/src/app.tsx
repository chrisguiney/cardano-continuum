import { useEffect, useState } from "preact/hooks";
import { getJSON } from "./lib/api";
import { navigate, parseRoute } from "./lib/router";
import { Actor } from "./views/Actor";
import { Actors } from "./views/Actors";
import { Controls } from "./views/Controls";
import { Decisions } from "./views/Decisions";
import { Findings } from "./views/Findings";
import { Overview } from "./views/Overview";
import { Pools } from "./views/Pools";

type Theme = "light" | "dark";

function initialTheme(): Theme {
  try {
    const saved = localStorage.getItem("theme");
    if (saved === "light" || saved === "dark") return saved;
  } catch {
    // storage can be blocked; fall through to the system preference.
  }
  return matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function useTheme(): [Theme, () => void] {
  const [theme, setTheme] = useState<Theme>(initialTheme);
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
  }, [theme]);
  const toggle = () =>
    setTheme((t) => {
      const next = t === "dark" ? "light" : "dark";
      try {
        localStorage.setItem("theme", next);
      } catch {
        // the choice then lasts for this page only.
      }
      return next;
    });
  return [theme, toggle];
}

function usePath(): string {
  const [path, setPath] = useState(location.pathname);
  useEffect(() => {
    const on = () => setPath(location.pathname);
    addEventListener("popstate", on);
    return () => removeEventListener("popstate", on);
  }, []);
  return path;
}

function useControlsEnabled(): boolean {
  const [on, setOn] = useState(false);
  useEffect(() => {
    getJSON<{ controls: boolean }>("/api/capabilities").then((c) => setOn(c.controls), () => setOn(false));
  }, []);
  return on;
}

const NAV: [string, string][] = [
  ["/", "Overview"],
  ["/actors", "Actors"],
  ["/pools", "Pools"],
  ["/decisions", "Decisions"],
  ["/findings", "Findings"],
];

export function App() {
  const path = usePath();
  const [theme, toggle] = useTheme();
  const controls = useControlsEnabled();
  const route = parseRoute(path);
  const nav = controls ? [...NAV, ["/controls", "Controls"] as [string, string]] : NAV;
  const active = (to: string) => (to === "/" ? path === "/" : path === to || path.startsWith(to + "/"));

  return (
    <>
      <a class="skip" href="#main">Skip to content</a>
      <header class="top">
        <strong class="brand">continuum</strong>
        <nav aria-label="Views">
          {nav.map(([to, label]) => (
            <a
              key={to}
              href={to}
              aria-current={active(to) ? "page" : undefined}
              onClick={(e) => {
                if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
                e.preventDefault();
                navigate(to);
              }}
            >
              {label}
            </a>
          ))}
        </nav>
        <button type="button" class="ghost" onClick={toggle} aria-label="Toggle light and dark theme">
          {theme === "dark" ? "Light theme" : "Dark theme"}
        </button>
      </header>
      <main id="main">
        {route.name === "overview" && <Overview />}
        {route.name === "actors" && <Actors />}
        {route.name === "actor" && <Actor id={route.id} />}
        {route.name === "pools" && <Pools />}
        {route.name === "decisions" && <Decisions />}
        {route.name === "findings" && <Findings />}
        {route.name === "controls" && (controls ? <Controls /> : <p class="empty">Controls are not enabled on this server (start it with --controls).</p>)}
        {route.name === "notfound" && <p class="empty">No such page.</p>}
      </main>
    </>
  );
}
