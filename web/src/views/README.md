# web/src/views

React components. One file per route: `Overview`, `Actors`, `Actor`, `Nodes`
(node processes), `StakePools` (on-chain pools),
`Explorer` (with `ExBlock`, `ExTx`, `ExAddress`, `ExEpoch`), `Governance`,
`GovAction`, `Markets`, `Contracts` and `ContractDetail` (a validator's compilation phases), `Decisions`, `Findings`, `Adversaries`, `Controls`, `Help`.
Shared pieces: `common.tsx` (headings, panels, table frames, facts lists,
banners, links), `startup.tsx` (the engine status provider and the startup banner), `fields.tsx` (inputs and the `HelpTip` tooltip),
`CodeBlock.tsx` (monospace text with a line-number gutter), `filterbars.tsx`, `exparts.tsx`, `govparts.tsx`, `SearchBox.tsx`, `AppSidebar.tsx` (the grouped left menu; Findings carries the count of new findings).
