import type { ReactNode } from "react";
import { Boxes, Compass, FileCode2, Gauge, Gavel, HelpCircle, ListChecks, Server, ShieldAlert, SlidersHorizontal, Store, Users, Waypoints, Bug } from "lucide-react";
import { Sidebar, SidebarCollapse, SidebarContent, SidebarFooter, SidebarGroup, SidebarHeader, SidebarItem, useSidebar } from "@/components/ui/sidebar";
import { isActive, navGroups, type Capabilities } from "../lib/nav";
import { navigate } from "../lib/router";

const ICONS: Record<string, ReactNode> = {
  "/": <Gauge />,
  "/nodes": <Server />,
  "/pools": <Waypoints />,
  "/explorer": <Compass />,
  "/actors": <Users />,
  "/decisions": <ListChecks />,
  "/markets": <Store />,
  "/contracts": <FileCode2 />,
  "/governance": <Gavel />,
  "/findings": <Bug />,
  "/adversaries": <ShieldAlert />,
  "/controls": <SlidersHorizontal />,
  "/help": <HelpCircle />,
};

/**
 * AppSidebar is the app's left menu: the name, the page groups the server
 * enables, and a collapse control. A count above zero badges Findings.
 */
export function AppSidebar({ path, caps, newFindings }: { path: string; caps: Capabilities; newFindings: number }) {
  const { setOpen } = useSidebar();
  const go = (to: string) => {
    setOpen(false);
    navigate(to);
  };
  const groups = navGroups(caps);
  const last = groups[groups.length - 1]!;
  const rest = groups.slice(0, -1);
  const item = (collapsed: boolean, to: string, label: string) => (
    <SidebarItem
      key={to}
      href={to}
      label={label}
      icon={ICONS[to] ?? <Boxes />}
      active={isActive(path, to)}
      collapsed={collapsed}
      badge={to === "/findings" && newFindings > 0 ? String(newFindings > 99 ? "99+" : newFindings) : undefined}
      badgeLabel={to === "/findings" && newFindings > 0 ? `${newFindings} new findings` : undefined}
      onNavigate={go}
    />
  );
  return (
    <Sidebar>
      {(collapsed) => (
        <>
          <SidebarHeader className={collapsed ? "justify-center px-0" : undefined}>
            <strong className="tracking-wide">{collapsed ? "c" : "continuum"}</strong>
          </SidebarHeader>
          <nav aria-label="Views" className="flex min-h-0 flex-1 flex-col">
            <SidebarContent>
              {rest.map((g, i) => (
                <SidebarGroup key={g.label ?? i} label={g.label} collapsed={collapsed}>
                  {g.items.map((n) => item(collapsed, n.path, n.label))}
                </SidebarGroup>
              ))}
            </SidebarContent>
            <div className="shrink-0 border-t p-2">
              <SidebarGroup collapsed={collapsed}>{last.items.map((n) => item(collapsed, n.path, n.label))}</SidebarGroup>
            </div>
          </nav>
          <SidebarFooter className="hidden md:flex">
            <SidebarCollapse collapsed={collapsed} />
          </SidebarFooter>
        </>
      )}
    </Sidebar>
  );
}
