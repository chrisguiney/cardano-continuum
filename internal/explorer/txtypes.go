package explorer

// AssetView is a native asset amount, or for a mint a signed quantity.
type AssetView struct {
	Policy   string `json:"policy"`
	Name     string `json:"name"`
	NameText string `json:"name_text,omitempty"`
	Quantity string `json:"quantity"`
}

// DatumView is an output's datum: Kind is "hash" or "inline".
type DatumView struct {
	Kind string `json:"kind"`
	Hash string `json:"hash"`
	// Hex is the inline datum's CBOR.
	Hex string `json:"hex,omitempty"`
}

// ScriptView is a script by hash and language.
type ScriptView struct {
	Hash     string `json:"hash"`
	Language string `json:"language"`
	Size     int    `json:"size"`
}

// OutputView is a transaction output.
type OutputView struct {
	Tx        string      `json:"tx,omitempty"`
	Index     uint32      `json:"index"`
	Address   string      `json:"address"`
	Lovelace  uint64      `json:"lovelace"`
	Assets    []AssetView `json:"assets,omitempty"`
	Datum     *DatumView  `json:"datum,omitempty"`
	ScriptRef *ScriptView `json:"script_ref,omitempty"`
}

// InputView is a spent or referenced output. Resolved is the output it
// names, nil when the index never saw it created.
type InputView struct {
	Tx       string      `json:"tx"`
	Index    uint32      `json:"index"`
	Resolved *OutputView `json:"resolved,omitempty"`
}

// CertView is a certificate decoded to the fields a reader wants. Kind is a
// snake_case name; unset fields are omitted.
type CertView struct {
	Kind       string `json:"kind"`
	Credential string `json:"credential,omitempty"`
	Pool       string `json:"pool,omitempty"`
	DRep       string `json:"drep,omitempty"`
	Amount     uint64 `json:"amount,omitempty"`
	Epoch      uint64 `json:"epoch,omitempty"`
	Anchor     string `json:"anchor,omitempty"`
}

// WithdrawalView is a reward withdrawal.
type WithdrawalView struct {
	Address  string `json:"address"`
	Lovelace uint64 `json:"lovelace"`
}

// RedeemerView is a redeemer with its declared execution budget.
type RedeemerView struct {
	Tag   string `json:"tag"`
	Index uint32 `json:"index"`
	Mem   uint64 `json:"mem"`
	Steps uint64 `json:"steps"`
	// DataHex is the redeemer datum's CBOR.
	DataHex string `json:"data_hex,omitempty"`
}

// VoteView is one vote a transaction casts.
type VoteView struct {
	Voter       string `json:"voter"`
	VoterHash   string `json:"voter_hash"`
	ActionTx    string `json:"action_tx"`
	ActionIndex uint32 `json:"action_index"`
	Choice      string `json:"choice"`
}

// ProposalView is a governance action a transaction proposes.
type ProposalView struct {
	Index   int    `json:"index"`
	Kind    string `json:"kind"`
	Deposit uint64 `json:"deposit"`
	Return  string `json:"return_address"`
	Anchor  string `json:"anchor,omitempty"`
}

// TxView is a transaction in full, or for one whose block is evicted only
// its header (Evicted).
type TxView struct {
	Hash   string `json:"hash"`
	Block  string `json:"block"`
	Height uint64 `json:"height"`
	Slot   uint64 `json:"slot"`
	Index  int    `json:"index"`
	// Evicted: the block's full contents are gone, so only the fields above
	// and Fee are known.
	Evicted bool   `json:"evicted"`
	Valid   bool   `json:"valid"`
	Size    int    `json:"size"`
	Fee     uint64 `json:"fee"`
	// TTL and ValidFrom are the validity interval's slots, nil when absent.
	TTL       *uint64 `json:"ttl,omitempty"`
	ValidFrom *uint64 `json:"valid_from,omitempty"`

	Inputs           []InputView      `json:"inputs"`
	Outputs          []OutputView     `json:"outputs"`
	RefInputs        []InputView      `json:"reference_inputs"`
	Collateral       []InputView      `json:"collateral"`
	CollateralReturn *OutputView      `json:"collateral_return,omitempty"`
	TotalCollateral  *uint64          `json:"total_collateral,omitempty"`
	Mint             []AssetView      `json:"mint"`
	Certs            []CertView       `json:"certificates"`
	Withdrawals      []WithdrawalView `json:"withdrawals"`
	Signers          []string         `json:"required_signers"`
	Metadata         map[string]any   `json:"metadata,omitempty"`
	Redeemers        []RedeemerView   `json:"redeemers"`
	Scripts          []ScriptView     `json:"scripts"`
	Votes            []VoteView       `json:"votes"`
	Proposals        []ProposalView   `json:"proposals"`
	CborHex          string           `json:"cbor_hex,omitempty"`
}

// TxRef is a transaction in an address's history.
type TxRef struct {
	Hash   string `json:"hash"`
	Block  string `json:"block"`
	Height uint64 `json:"height"`
	Slot   uint64 `json:"slot"`
}

// AddressView is an address's unspent outputs, balance and history.
type AddressView struct {
	Address string `json:"address"`
	// Stake is the stake address the payment address delegates to, if it has
	// one.
	Stake   string       `json:"stake,omitempty"`
	Balance uint64       `json:"balance"`
	Assets  []AssetView  `json:"assets"`
	Utxos   []OutputView `json:"utxos"`
	// UtxoTotal and TxCount are the full counts; Utxos and History hold
	// the requested page of each.
	UtxoTotal int     `json:"utxo_total"`
	TxCount   int     `json:"tx_count"`
	History   []TxRef `json:"history"`
	Offset    int     `json:"offset"`
	Limit     int     `json:"limit"`
}
