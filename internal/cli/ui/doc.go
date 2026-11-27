// Package ui holds continuum-ui's commands, which serve the web UI apart
// from the engine binary so a UI change never changes the engine image.
//
//	browser --> continuum-ui proxy --/api/*--> engine (continuum run --serve)
//	browser --> continuum-ui serve --log      (continuum serve's API in process)
//
// proxy serves the embedded build (package web) and forwards /api/* to an
// engine's live API. serve is continuum serve with the UI beside its API, for
// viewing a recorded run.
package ui
