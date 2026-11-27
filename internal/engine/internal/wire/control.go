package wire

// Control is httpapi.Control as the log carries it. The JSON names are the
// log format; the engine cannot import httpapi, which reads engine bodies.
type Control struct {
	RequestID string             `json:"request_id"`
	Op        string             `json:"op"`
	K         uint32             `json:"k,omitzero"`
	Weights   map[string]float64 `json:"weights,omitempty"`
	Scenario  string             `json:"scenario,omitempty"`
}
