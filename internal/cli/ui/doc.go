// Package ui holds continuum-ui's commands, which serve the web UI apart
// from the engine binary so a UI change never changes the engine image.
//
//	browser --> continuum-ui proxy --/api/*--> engine (continuum run --serve)
//	browser --> continuum-ui serve --log      (continuum serve's API in process)
//
// proxy serves the embedded build (package web) and forwards /api/* to an
// engine's live API. serve is continuum serve with the UI beside its API, for
// viewing a recorded run.
//
// proxy answers /api/_ui/status itself: the engine as "up", "starting" or
// "unreachable" from a cached probe, so the UI can tell a starting devnet
// from a broken one while /api/* still answers 502.
package ui
