# web/src

- `main.tsx`: mounts `App` and loads `index.css`.
- `app.tsx`: the shell (left sidebar, top bar with search and style select); picks a view from the
  route and loads every view but the overview on first visit.
- `index.css`: Tailwind, the colour tokens for both themes and the dark
  variant. See [`../README.md`](../README.md#styling).
- [`components/ui/`](components/ui/README.md): shadcn/ui components.
- [`lib/`](lib/README.md): API access, types, routing and view logic.
- [`views/`](views/README.md): one component per route and their shared parts.
