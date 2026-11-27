package wire

// Control is the body of a Control input: an operator's request to steer
// the run. The JSON names are the log format. It is the one log shape of a
// control; the HTTP API's request type converts to it.
type Control struct {
	// RequestID makes the request idempotent.
	RequestID string `json:"request_id"`
	// Op is the operation (httpapi.Op).
	Op string `json:"op"`
	// K is the amplification factor.
	K uint32 `json:"k,omitzero"`
	// Weights override intent weights by name.
	Weights map[string]float64 `json:"weights,omitempty"`
	// Scenario names a scenario to inject.
	Scenario string `json:"scenario,omitempty"`
}
