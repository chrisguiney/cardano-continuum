import { useEffect, useState } from "react";
import { MoonIcon, SunIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { TooltipProvider } from "@/components/ui/tooltip";
import { getJSON } from "./lib/api";
import { navigate, parseRoute } from "./lib/router";
import { Actor } from "./views/Actor";
import { Actors } from "./views/Actors";
import { Adversaries } from "./views/Adversaries";
import { Controls } from "./views/Controls";
import { Decisions } from "./views/Decisions";
import { Findings } from "./views/Findings";
import { Help } from "./views/Help";
import { HelpTip } from "./views/fields";
import { FIELD_HELP } from "./lib/help";
import { Governance } from "./views/Governance";
import { GovAction } from "./views/GovAction";
import { Overview } from "./views/Overview";
import { ExAddress } from "./views/ExAddress";
import { ExBlock } from "./views/ExBlock";
import { ExEpoch } from "./views/ExEpoch";
import { ExTx } from "./views/ExTx";
import { Explorer } from "./views/Explorer";
import { SearchBox } from "./views/SearchBox";
import { Note } from "./views/common";
import { navFor, type Capabilities } from "./lib/nav";
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

function useCapabilities(): Capabilities {
  const [caps, setCaps] = useState<Capabilities>({ controls: false, explorer: false });
  useEffect(() => {
    getJSON<Partial<Capabilities>>("/api/capabilities").then(
      (c) => setCaps({ controls: !!c.controls, explorer: !!c.explorer }),
      () => setCaps({ controls: false, explorer: false }),
    );
  }, []);
  return caps;
}

export function App() {
  const path = usePath();
  const [theme, toggle] = useTheme();
  const caps = useCapabilities();
  const controls = caps.controls;
  const route = parseRoute(path);
  const nav = navFor(caps);
  const active = (to: string) => (to === "/" ? path === "/" : path === to || path.startsWith(to + "/"));

  return (
    <TooltipProvider>
      <a href="#main" className="absolute -left-[9999px] z-10 rounded-md bg-card p-2 focus:top-2 focus:left-2">Skip to content</a>
      <header className="flex flex-wrap items-center gap-4 border-b bg-card px-4 py-2">
        <strong className="mr-auto tracking-wide lg:mr-0">continuum</strong>
        <nav aria-label="Views" className="order-last flex basis-full flex-wrap gap-1 lg:order-none lg:basis-auto lg:flex-1">
          {nav.map(([to, label]) => (
            <a
              key={to}
              href={to}
              aria-current={active(to) ? "page" : undefined}
              className="rounded-md px-3 py-1.5 hover:bg-accent aria-[current=page]:bg-primary aria-[current=page]:font-semibold aria-[current=page]:text-primary-foreground"
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
        {caps.explorer && <SearchBox />}
        <span className="inline-flex items-center">
          <Button type="button" variant="ghost" size="sm" onClick={toggle} aria-describedby="theme-help">
            {theme === "dark" ? <SunIcon aria-hidden="true" /> : <MoonIcon aria-hidden="true" />}
            {theme === "dark" ? "Light theme" : "Dark theme"}
          </Button>
          <HelpTip id="theme-help" label="Theme" text={FIELD_HELP.theme!.help} />
        </span>
      </header>
      <main id="main" className="mx-auto max-w-[1200px] px-4 pb-12">
        {route.name === "overview" && <Overview />}
        {route.name === "actors" && <Actors />}
        {route.name === "actor" && <Actor id={route.id} />}
        {route.name === "pools" && <Pools />}
        {route.name === "governance" && <Governance />}
        {route.name === "govaction" && <GovAction tx={route.tx} index={route.index} />}
        {route.name === "decisions" && <Decisions />}
        {route.name === "findings" && <Findings />}
        {route.name === "adversaries" && <Adversaries />}
        {route.name === "explorer" && <Explorer />}
        {route.name === "xblock" && <ExBlock id={route.key} />}
        {route.name === "xtx" && <ExTx id={route.key} />}
        {route.name === "xaddr" && <ExAddress addr={route.key} />}
        {route.name === "xepoch" && <ExEpoch n={route.n} />}
        {route.name === "xpool" && <Explorer pool={route.key} />}
        {route.name === "help" && <Help />}
        {route.name === "controls" && (controls ? <Controls /> : <Note className="p-4 text-center">Controls are not enabled on this server (start it with --controls).</Note>)}
        {route.name === "notfound" && <Note className="p-4 text-center">No such page.</Note>}
      </main>
    </TooltipProvider>
  );
}
