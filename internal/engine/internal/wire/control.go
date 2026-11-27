package wire

// Control is httpapi.Control as the log carries it. The CBOR keys are the
// log format; the engine cannot import httpapi, which reads engine bodies.
type Control struct {
	RequestID string             `cbor:"1,keyasint"`
	Op        string             `cbor:"2,keyasint"`
	K         uint32             `cbor:"3,keyasint,omitempty"`
	Weights   map[string]float64 `cbor:"4,keyasint,omitempty"`
	Scenario  string             `cbor:"5,keyasint,omitempty"`
}
