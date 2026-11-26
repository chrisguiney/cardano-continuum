// Package view is the read side of the web API: the response types, the
// Projection that folds an event log into them, and the LogSource that
// answers the API's Source from a Projection, the store and the operator.
// It does no HTTP and never writes to the log.
package view
