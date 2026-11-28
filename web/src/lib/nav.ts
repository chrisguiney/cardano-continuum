export type NavItem = [path: string, label: string];

export interface Capabilities {
  controls: boolean;
  explorer: boolean;
}

const BASE: NavItem[] = [
  ["/", "Overview"],
  ["/actors", "Actors"],
  ["/nodes", "Nodes"],
  ["/pools", "Stake pools"],
];

const TAIL: NavItem[] = [
  ["/governance", "Governance"],
  ["/decisions", "Decisions"],
  ["/findings", "Findings"],
  ["/adversaries", "Adversaries"],
];

/** navFor lists the top menu. Help is always last, whatever else the server enables. */
export function navFor(caps: Capabilities): NavItem[] {
  return [
    ...BASE,
    ...(caps.explorer ? ([["/explorer", "Explorer"]] as NavItem[]) : []),
    ...TAIL,
    ...(caps.controls ? ([["/controls", "Controls"]] as NavItem[]) : []),
    ["/help", "Help"],
  ];
}
