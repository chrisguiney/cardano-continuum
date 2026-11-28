import { ada, count, simTime } from "./format";
import type { ActorDetail } from "./types";

export interface Row {
  label: string;
  value: string;
  /** copy marks an address or id the page offers a copy button for. */
  copy?: boolean;
}
export interface Section {
  id: string;
  title: string;
  rows: Row[];
  note?: string;
}

const pct = (x: number) => x.toFixed(3);

/** detailSections arranges an actor's detail into the sections the page renders, in reading order. */
export function detailSections(d: ActorDetail): Section[] {
  const none = "none";
  const out: Section[] = [
    {
      id: "identity", title: "Identity",
      note: d.has_identity ? undefined : "No store row supplies wallet, holdings, delegation or traits; serve with --store to show them.",
      rows: [
        { label: "Actor id", value: String(d.id) },
        { label: "Kind", value: d.kind || "unknown" },
        { label: "Name", value: d.name || "unnamed" },
        { label: "Profile", value: d.profile || "unknown" },
        { label: "Agenda", value: d.agenda || "unknown" },
      ],
    },
    {
      id: "wallet", title: "Wallet",
      rows: [
        { label: "Payment address", value: d.wallet.payment_addr, copy: true },
        { label: "Stake address", value: d.wallet.stake_addr, copy: true },
        { label: "Payment key hash", value: d.wallet.payment_key_hash, copy: true },
        { label: "Stake key hash", value: d.wallet.stake_key_hash, copy: true },
        ...(d.wallet.drep_id ? [{ label: "DRep id", value: d.wallet.drep_id, copy: true }] : []),
        ...(d.wallet.pool_id ? [{ label: "Pool id", value: d.wallet.pool_id, copy: true }] : []),
      ].filter((r) => r.value !== ""),
    },
    {
      id: "holdings", title: "Holdings",
      note: "Settled and reserved are at the settled point; tokens are at the chain tip; all as of the last checkpoint.",
      rows: [
        { label: "Settled", value: ada(d.holdings.settled_lovelace) },
        { label: "Reserved by in-flight payments", value: ada(d.holdings.reserved_lovelace) },
        { label: "Unconfirmed (in flight)", value: ada(d.holdings.unconfirmed_lovelace) },
        { label: "UTxOs", value: count(d.holdings.utxos) },
        { label: "Payments in flight", value: count(d.holdings.in_flight) },
      ],
    },
    {
      id: "delegation", title: "Delegation",
      rows: [
        { label: "Stake key registered", value: d.delegation.stake_registered ? "yes" : "no" },
        { label: "Pool", value: d.delegation.pool || none, copy: !!d.delegation.pool },
        { label: "DRep", value: d.delegation.drep || none, copy: !!d.delegation.drep && !d.delegation.drep.startsWith("always_") },
      ],
    },
    {
      id: "lifecycle", title: "Lifecycle",
      rows: [
        { label: "State", value: d.lifecycle.state || "unknown" },
        { label: "Born", value: simTime(d.lifecycle.born_ms) },
        { label: "Last transition", value: d.lifecycle.changed_ms > 0 ? simTime(d.lifecycle.changed_ms) : "never" },
        { label: "Dormancy hazard (per s)", value: d.lifecycle.hazard.toExponential(2) },
        { label: "Frustration", value: pct(d.lifecycle.frustration) },
      ],
    },
    {
      id: "traits", title: "Traits",
      rows: [
        { label: "Competence", value: pct(d.traits.competence) },
        { label: "Care", value: pct(d.traits.care) },
        { label: "Patience", value: pct(d.traits.patience) },
        { label: "Learning rate", value: pct(d.traits.learning_rate) },
        ...(d.market ? [{ label: "Loss aversion", value: pct(d.traits.loss_aversion ?? 0) }] : []),
      ],
    },
    {
      id: "activity", title: "Activity",
      rows: [
        { label: "Intents chosen", value: count(d.activity.intents) },
        { label: "Submitted", value: count(d.activity.submitted) },
        { label: "Confirmed", value: count(d.activity.confirmed) },
        { label: "Refused", value: count(d.activity.refused) },
        { label: "Released", value: count(d.activity.released) },
        { label: "Gave up", value: count(d.activity.gave_up) },
        { label: "Retries", value: count(d.activity.retries) },
        { label: "Last activity", value: d.activity.last_ms > 0 ? simTime(d.activity.last_ms) : "never" },
        { label: "Lost to mistakes", value: ada(d.activity.lost_lovelace) },
      ],
    },
    {
      id: "findings", title: "Findings",
      rows: d.findings.map((f) => ({ label: `${f.severity} ${f.kind}`, value: f.summary })),
    },
  ];
  if (d.adversary) {
    out.splice(7, 0, {
      id: "adversary", title: "Adversary",
      rows: [
        { label: "Cost spent", value: ada(d.adversary.spent) },
        { label: "Ceiling per epoch", value: ada(d.adversary.ceiling) },
        { label: "Success metric", value: d.adversary.metric || "unset" },
      ],
    });
  }
  if (d.market) {
    const m = d.market;
    out.splice(3, 0, {
      id: "market", title: "Market",
      note: "Cost basis is the average price paid per ADA in the primary currency, as of the last checkpoint.",
      rows: [
        { label: "Primary currency", value: m.primary },
        { label: "Other currencies", value: m.extra?.length ? m.extra.join(", ") : none },
        { label: "Cost basis per ADA", value: `${m.basis.toPrecision(4)} ${m.primary}` },
        { label: "ADA the basis covers", value: ada(m.units) },
        { label: "Last exchange", value: m.venue || none },
      ],
    });
  }
  return out;
}
