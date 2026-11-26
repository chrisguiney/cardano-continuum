// Package httpapi is the web UI's HTTP API: REST snapshots of a run and a
// server-sent-events feed of the engine's decisions.
//
// The handlers read through Source and write only through Controls, so the
// API never touches engine state: a control is appended to the event log as
// a Control input and the engine acts on it from there, which keeps a
// controlled run replayable. There is no authentication; the server binds to
// loopback by default and the access path is a port-forward.
//
//	┌──────────┐ Poll  ┌────────────┐ Publish ┌─────┐ SSE
//	│ events   │──────▶│ Tail       │────────▶│ Hub │────────▶ clients
//	│ .log     │       │ Projection │         └─────┘
//	└──────────┘       └─────┬──────┘
//	                         │ queries
//	      store, kube ──▶ LogSource ──▶ Source ──▶ handlers ◀── POST /api/control
//	                                                   └────▶ Controls ──▶ log
//
// The packages form a DAG, each importing only those below it:
//
//	httpapi         handlers, controls, the explorer
//	  ├─▶ stream    Hub, Tail and Feed: the decision feed
//	  │     └─▶ view
//	  └─▶ view      Projection, LogSource, Source and the response types
//
// view.LogSource is the read-only Source over a log file, the store's actor
// table and the operator's pool status. A live engine supplies its own
// Source and Controls through the same interfaces.
package httpapi
