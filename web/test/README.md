# web/test

Vitest unit tests for `src/lib` and the views, named for the area they cover
(`actors`, `adversaries`, `chance`, `controls`, `explorer`, `findings`,
`forks`, `governance`, `help`, `paging`, `feed`, `tooltip`, `misc`). Most
render a view to a string with `react-dom/server`; a file that needs a DOM
(keyboard focus, a dialog, following `aria-describedby`) starts with
`// @vitest-environment jsdom` and uses Testing Library. `setup.ts` cleans up
after those and stubs what jsdom lacks. Run with `make web-test` (typecheck
first) or `npm test` in `web/`.
