# web/src/lib

Non-visual code.

- API: `api.ts` (`getJSON`), `hooks.ts` (`usePoll`), `meta.ts` (the server's
  enum sets), `feed.ts` (the decision event stream), `query.ts`,
  `paging.ts`, `search.ts`, `controls.ts`.
- Types: `types.ts` mirrors the JSON of `internal/httpapi`; `extypes.ts` the
  explorer's responses.
- Routing: `router.ts` (`parseRoute` validates each route's argument), `nav.ts`.
- Per-view logic: `actorcols.ts`, `actordetail.ts`, `adversaries.ts`,
  `chance.ts`, `explorer.ts`, `findings.ts`, `forks.ts`, `governance.ts`,
  `govdetail.ts`.
- Help text: `help.ts`, `advhelp.ts`, `govhelp.ts` (field help and glossaries).
- Utilities: `format.ts`, `colors.ts`, `layout.ts`, `clipboard.ts`, `utils.ts`
  (`cn`, which shadcn/ui components import from `@/lib/utils`).

Unit tests are in [`../../test/`](../../test/README.md).
