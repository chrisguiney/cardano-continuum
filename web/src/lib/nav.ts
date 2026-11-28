/** NavItem is one sidebar link. */
export interface NavItem {
  path: string;
  label: string;
}

/** NavGroup is a headed run of links; an unlabelled group has no heading. */
export interface NavGroup {
  label?: string;
  items: NavItem[];
}

export interface Capabilities {
  controls: boolean;
  explorer: boolean;
}

const item = (path: string, label: string): NavItem => ({ path, label });

/**
 * navGroups lists the sidebar: Overview first, then the headed groups, and
 * Help alone at the end whatever else the server enables. A group the
 * server enables nothing in is left out.
 */
export function navGroups(caps: Capabilities): NavGroup[] {
  const groups: NavGroup[] = [
    { items: [item("/", "Overview")] },
    { label: "Network", items: [item("/nodes", "Nodes"), item("/pools", "Stake pools"), ...(caps.explorer ? [item("/explorer", "Explorer")] : [])] },
    {
      label: "Activity",
      items: [item("/actors", "Actors"), item("/people", "People"), item("/decisions", "Decisions"), item("/markets", "Markets"), ...(caps.explorer ? [item("/contracts", "Contracts")] : []), item("/governance", "Governance")],
    },
    { label: "Testing", items: [item("/findings", "Findings"), item("/adversaries", "Adversaries")] },
    { label: "Run", items: caps.controls ? [item("/controls", "Controls")] : [] },
    { items: [item("/help", "Help")] },
  ];
  return groups.filter((g) => g.items.length > 0);
}

/** isActive reports whether the page at path belongs to the link to. */
export function isActive(path: string, to: string): boolean {
  return to === "/" ? path === "/" : path === to || path.startsWith(to + "/");
}
