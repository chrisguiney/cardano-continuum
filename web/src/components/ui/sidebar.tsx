import { createContext, useContext, useMemo, useState, type ComponentProps, type ReactNode } from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { Menu, PanelLeftClose, PanelLeftOpen } from "lucide-react";
import { loadCollapsed, saveCollapsed } from "@/lib/sidebar";
import { cn } from "@/lib/utils";
import { Tooltip, TooltipContent, TooltipTrigger } from "./tooltip";

interface SidebarState {
  /** collapsed is the desktop sidebar's icon-only mode. */
  collapsed: boolean;
  toggle: () => void;
  /** open is whether the narrow-screen sheet is showing. */
  open: boolean;
  setOpen: (o: boolean) => void;
}

const Ctx = createContext<SidebarState | null>(null);

/** useSidebar reads the sidebar's state; it must sit under a SidebarProvider. */
function useSidebar(): SidebarState {
  const s = useContext(Ctx);
  if (!s) throw new Error("useSidebar needs a SidebarProvider");
  return s;
}

/**
 * SidebarProvider holds the collapsed choice (remembered in localStorage)
 * and whether the narrow-screen sheet is open.
 */
function SidebarProvider({ children }: { children: ReactNode }) {
  const [collapsed, setCollapsed] = useState(loadCollapsed);
  const [open, setOpen] = useState(false);
  const value = useMemo<SidebarState>(
    () => ({
      collapsed,
      open,
      setOpen,
      toggle: () => {
        saveCollapsed(!collapsed);
        setCollapsed(!collapsed);
      },
    }),
    [collapsed, open],
  );
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

/**
 * Sidebar is the fixed column from md up and, below it, a sheet that opens
 * from the left. Both show the same children; the sheet is never collapsed.
 * children receives whether the column is icon-only.
 */
function Sidebar({ children }: { children: (collapsed: boolean) => ReactNode }) {
  const { collapsed, open, setOpen } = useSidebar();
  return (
    <>
      <aside
        data-slot="sidebar"
        data-collapsed={collapsed}
        className={cn(
          "sticky top-0 z-20 hidden h-screen shrink-0 flex-col border-r bg-card md:flex",
          collapsed ? "w-14" : "w-56",
        )}
      >
        {children(collapsed)}
      </aside>
      <DialogPrimitive.Root open={open} onOpenChange={setOpen}>
        <DialogPrimitive.Portal>
          <DialogPrimitive.Overlay className="fixed inset-0 z-40 bg-black/60 md:hidden" />
          <DialogPrimitive.Content
            data-slot="sidebar-sheet"
            aria-describedby={undefined}
            className="fixed inset-y-0 left-0 z-50 flex w-64 max-w-[85vw] flex-col border-r bg-card shadow-lg md:hidden"
          >
            <DialogPrimitive.Title className="sr-only">Menu</DialogPrimitive.Title>
            {children(false)}
          </DialogPrimitive.Content>
        </DialogPrimitive.Portal>
      </DialogPrimitive.Root>
    </>
  );
}

/** SidebarTrigger is the top bar's button that opens the sheet; md and wider hide it. */
function SidebarTrigger({ className, ...props }: ComponentProps<"button">) {
  const { setOpen } = useSidebar();
  return (
    <button
      type="button"
      data-slot="sidebar-trigger"
      aria-label="Open menu"
      onClick={() => setOpen(true)}
      className={cn("inline-flex size-9 items-center justify-center rounded-md hover:bg-accent md:hidden", className)}
      {...props}
    >
      <Menu aria-hidden className="size-5" />
    </button>
  );
}

/** SidebarCollapse switches the column between full width and icons only. */
function SidebarCollapse({ collapsed }: { collapsed: boolean }) {
  const { toggle } = useSidebar();
  const Icon = collapsed ? PanelLeftOpen : PanelLeftClose;
  const label = collapsed ? "Expand sidebar" : "Collapse sidebar";
  return (
    <button
      type="button"
      data-slot="sidebar-collapse"
      aria-label={label}
      aria-expanded={!collapsed}
      onClick={toggle}
      className={cn("hidden h-9 items-center gap-3 rounded-md px-2.5 text-muted-foreground hover:bg-accent hover:text-accent-foreground md:flex", collapsed && "justify-center")}
    >
      <Icon aria-hidden className="size-4 shrink-0" />
      {!collapsed && <span>Collapse</span>}
    </button>
  );
}

function SidebarHeader({ className, ...props }: ComponentProps<"div">) {
  return <div data-slot="sidebar-header" className={cn("flex h-12 shrink-0 items-center border-b px-3", className)} {...props} />;
}

function SidebarContent({ className, ...props }: ComponentProps<"div">) {
  return <div data-slot="sidebar-content" className={cn("flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto p-2", className)} {...props} />;
}

function SidebarFooter({ className, ...props }: ComponentProps<"div">) {
  return <div data-slot="sidebar-footer" className={cn("flex shrink-0 flex-col gap-1 border-t p-2", className)} {...props} />;
}

/** SidebarGroup is a headed list; the heading is dropped (a divider stands in) when collapsed. */
function SidebarGroup({ label, collapsed, children }: { label?: string; collapsed: boolean; children: ReactNode }) {
  return (
    <div data-slot="sidebar-group" className="flex flex-col gap-1">
      {label && !collapsed && <h2 className="px-2.5 pt-1 text-[11px] font-semibold tracking-wider text-muted-foreground uppercase">{label}</h2>}
      {label && collapsed && <hr className="mx-2 border-border" />}
      <ul className="flex flex-col gap-0.5">{children}</ul>
    </div>
  );
}

/**
 * SidebarItem is one link. Collapsed, the label stays for assistive tech and
 * shows in a tooltip on hover and keyboard focus.
 */
function SidebarItem({
  href,
  label,
  icon,
  active,
  collapsed,
  badge,
  badgeLabel,
  onNavigate,
}: {
  href: string;
  label: string;
  icon: ReactNode;
  active: boolean;
  collapsed: boolean;
  badge?: string;
  badgeLabel?: string;
  onNavigate: (href: string) => void;
}) {
  const link = (
    <a
      href={href}
      aria-current={active ? "page" : undefined}
      data-slot="sidebar-item"
      className={cn(
        "relative flex h-9 items-center gap-3 rounded-md px-2.5 hover:bg-accent hover:text-accent-foreground aria-[current=page]:bg-primary aria-[current=page]:font-semibold aria-[current=page]:text-primary-foreground",
        collapsed && "justify-center",
      )}
      onClick={(e) => {
        if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
        e.preventDefault();
        onNavigate(href);
      }}
    >
      <span aria-hidden className="shrink-0 [&>svg]:size-4">{icon}</span>
      <span className={cn("truncate", collapsed && "sr-only")}>{label}</span>
      {badge && (
        <span
          aria-label={badgeLabel}
          className={cn(
            "ml-auto rounded-full bg-warn px-1.5 text-xs leading-5 font-semibold text-primary-foreground",
            collapsed && "absolute top-0.5 right-0.5 ml-0 px-1 text-[10px] leading-4",
          )}
        >
          {badge}
        </span>
      )}
    </a>
  );
  return (
    <li>
      {collapsed ? (
        <Tooltip>
          <TooltipTrigger asChild>{link}</TooltipTrigger>
          <TooltipContent side="right">{badgeLabel ? `${label}: ${badgeLabel}` : label}</TooltipContent>
        </Tooltip>
      ) : (
        link
      )}
    </li>
  );
}

export { Sidebar, SidebarCollapse, SidebarContent, SidebarFooter, SidebarGroup, SidebarHeader, SidebarItem, SidebarProvider, SidebarTrigger, useSidebar };
