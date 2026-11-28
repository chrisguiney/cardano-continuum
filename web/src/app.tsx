import { lazy, Suspense, useEffect, useState } from "react";
import { TooltipProvider } from "@/components/ui/tooltip";
import { getJSON } from "./lib/api";
import { navigate, parseRoute, redirectFor } from "./lib/router";
import { Overview } from "./views/Overview";
import { SearchBox } from "./views/SearchBox";
import { Note } from "./views/common";
import { EngineProvider, StartupBanner, useEngine } from "./views/startup";
import { loadStyle, saveStyle, type Style } from "./lib/appearance";
import { StyleSelect } from "./views/StyleSelect";
import { navFor, type Capabilities } from "./lib/nav";

// Every view but the overview loads on first visit, so the landing page does
// not wait for the explorer's or the controls' code.
const Actor = lazy(() => import("./views/Actor").then((m) => ({ default: m.Actor })));
const Actors = lazy(() => import("./views/Actors").then((m) => ({ default: m.Actors })));
const Adversaries = lazy(() => import("./views/Adversaries").then((m) => ({ default: m.Adversaries })));
const Contracts = lazy(() => import("./views/Contracts").then((m) => ({ default: m.Contracts })));
const Controls = lazy(() => import("./views/Controls").then((m) => ({ default: m.Controls })));
const Decisions = lazy(() => import("./views/Decisions").then((m) => ({ default: m.Decisions })));
const ExAddress = lazy(() => import("./views/ExAddress").then((m) => ({ default: m.ExAddress })));
const ExBlock = lazy(() => import("./views/ExBlock").then((m) => ({ default: m.ExBlock })));
const ExEpoch = lazy(() => import("./views/ExEpoch").then((m) => ({ default: m.ExEpoch })));
const ExTx = lazy(() => import("./views/ExTx").then((m) => ({ default: m.ExTx })));
const Explorer = lazy(() => import("./views/Explorer").then((m) => ({ default: m.Explorer })));
const Findings = lazy(() => import("./views/Findings").then((m) => ({ default: m.Findings })));
const GovAction = lazy(() => import("./views/GovAction").then((m) => ({ default: m.GovAction })));
const Markets = lazy(() => import("./views/Markets").then((m) => ({ default: m.Markets })));
const Governance = lazy(() => import("./views/Governance").then((m) => ({ default: m.Governance })));
const Help = lazy(() => import("./views/Help").then((m) => ({ default: m.Help })));
const Nodes = lazy(() => import("./views/Nodes").then((m) => ({ default: m.Nodes })));
const StakePools = lazy(() => import("./views/StakePools").then((m) => ({ default: m.StakePools })));

function useStyle(): [Style, (s: Style) => void] {
  const [style, setStyle] = useState<Style>(loadStyle);
  useEffect(() => {
    document.documentElement.dataset.style = style;
  }, [style]);
  const choose = (s: Style) => {
    saveStyle(s);
    setStyle(s);
  };
  return [style, choose];
}

function usePath(): string {
  const [path, setPath] = useState(location.pathname);
  useEffect(() => {
    const on = () => setPath(location.pathname);
    addEventListener("popstate", on);
    return () => removeEventListener("popstate", on);
  }, []);
  // An old link is replaced, not pushed, so Back does not return to it.
  useEffect(() => {
    const to = redirectFor(location.pathname, location.search);
    if (!to) return;
    history.replaceState(null, "", to);
    setPath(location.pathname);
  }, [path]);
  return path;
}

function useCapabilities(phase: string): Capabilities {
  const [caps, setCaps] = useState<Capabilities>({ controls: false, explorer: false });
  useEffect(() => {
    getJSON<Partial<Capabilities>>("/api/capabilities").then(
      (c) => setCaps({ controls: !!c.controls, explorer: !!c.explorer }),
      () => setCaps({ controls: false, explorer: false }),
    );
  }, [phase === "up"]);
  return caps;
}

export function App() {
  return (
    <EngineProvider>
      <Shell />
    </EngineProvider>
  );
}

function Shell() {
  const path = usePath();
  const { phase } = useEngine();
  const caps = useCapabilities(phase);
  const [style, chooseStyle] = useStyle();
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
        <div className="ml-auto flex flex-wrap items-center gap-4">
          {caps.explorer && <SearchBox />}
          <StyleSelect style={style} onChange={chooseStyle} />
        </div>
      </header>
      <main id="main" className="mx-auto max-w-[1200px] px-4 pb-12">
        <StartupBanner />
        <Suspense fallback={<Note>Loading…</Note>}>
          {route.name === "overview" && <Overview />}
          {route.name === "actors" && <Actors />}
          {route.name === "actor" && <Actor id={route.id} />}
          {route.name === "nodes" && <Nodes />}
          {route.name === "node" && <Nodes name={route.key} />}
          {route.name === "pools" && <StakePools />}
          {route.name === "pool" && <StakePools id={route.key} />}
          {route.name === "governance" && <Governance />}
          {route.name === "markets" && <Markets />}
          {route.name === "govaction" && <GovAction tx={route.tx} index={route.index} />}
          {route.name === "decisions" && <Decisions />}
          {route.name === "findings" && <Findings />}
          {route.name === "adversaries" && <Adversaries />}
          {route.name === "explorer" && <Explorer />}
          {route.name === "contracts" && <Contracts />}
          {route.name === "xblock" && <ExBlock id={route.key} />}
          {route.name === "xtx" && <ExTx id={route.key} />}
          {route.name === "xaddr" && <ExAddress addr={route.key} />}
          {route.name === "xepoch" && <ExEpoch n={route.n} />}
          {route.name === "xpool" && <Explorer pool={route.key} />}
          {route.name === "help" && <Help />}
          {route.name === "controls" && (controls ? <Controls /> : <Note className="p-4 text-center">Controls are not enabled on this server (start it with --controls).</Note>)}
          {route.name === "notfound" && <Note className="p-4 text-center">No such page.</Note>}
        </Suspense>
      </main>
    </TooltipProvider>
  );
}
