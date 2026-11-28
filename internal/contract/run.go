package contract

// Run is one script execution a transaction asks for: one redeemer, the
// script it runs and what evaluating it gave. The engine records one per
// redeemer of each script transaction it submits.
type Run struct {
	// Validator is the registry validator the script is an instance of,
	// empty for a script the registry does not know.
	Validator string `json:"validator,omitempty"`
	// Instance names the deployed instance ("vesting#3"), empty for an
	// unparameterised validator.
	Instance string `json:"instance,omitempty"`
	// Script is the script hash in hex.
	Script string `json:"script"`
	// Purpose is the redeemer's purpose: spend, mint, ...
	Purpose Purpose `json:"purpose"`
	// Index is the redeemer's index among its purpose's targets.
	Index uint32 `json:"index"`
	// Datum is a spend's datum as CBOR hex, empty for other purposes.
	Datum string `json:"datum,omitempty"`
	// DatumJSON is Datum in the detailed-schema JSON cardano-cli uses.
	DatumJSON string `json:"datum_json,omitempty"`
	// Redeemer is the redeemer as CBOR hex.
	Redeemer string `json:"redeemer"`
	// RedeemerJSON is Redeemer in the detailed-schema JSON.
	RedeemerJSON string `json:"redeemer_json,omitempty"`
	// Declared are the execution units the redeemer declares.
	Declared Units `json:"declared"`
	// Used are the units evaluation spent; zero when it failed before
	// reporting them.
	Used Units `json:"used"`
	// OK reports that the script accepted within the declared units.
	OK bool `json:"ok"`
	// Error is why it did not.
	Error string `json:"error,omitempty"`
}

// Units are execution units: memory and CPU steps.
type Units struct {
	// Mem is memory units.
	Mem int64 `json:"mem"`
	// Steps is CPU steps.
	Steps int64 `json:"steps"`
}
