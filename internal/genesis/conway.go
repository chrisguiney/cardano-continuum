package genesis

import (
	"encoding/hex"
	"encoding/json"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

// committeeExpiry is the epoch at which genesis committee members expire:
// far enough that a devnet never reaches it.
const committeeExpiry = 100_000

// The field names follow cardano-ledger's ToJSON ConwayGenesis
// (Cardano/Ledger/Conway/Genesis.hs).
type conwayDoc struct {
	CommitteeDoc        committeeDoc    `json:"committee"`
	CommitteeMaxTerm    int             `json:"committeeMaxTermLength"`
	CommitteeMinSize    int             `json:"committeeMinSize"`
	Constitution        constitutionDoc `json:"constitution"`
	DRepActivity        int             `json:"dRepActivity"`
	DRepDeposit         uint64          `json:"dRepDeposit"`
	DRepThresholds      drepThresholds  `json:"dRepVotingThresholds"`
	GovActionDeposit    uint64          `json:"govActionDeposit"`
	GovActionLifetime   int             `json:"govActionLifetime"`
	MinFeeRefScriptCost json.Number     `json:"minFeeRefScriptCostPerByte"`
	PlutusV3CostModel   []int64         `json:"plutusV3CostModel"`
	PoolThresholds      poolThresholds  `json:"poolVotingThresholds"`
}

type committeeDoc struct {
	Members   map[string]int `json:"members"`
	Threshold struct {
		Numerator   int `json:"numerator"`
		Denominator int `json:"denominator"`
	} `json:"threshold"`
}

// constitutionDoc has no guardrail script, so parameter-change and
// treasury proposals need no script witness on a devnet.
type constitutionDoc struct {
	Anchor struct {
		DataHash string `json:"dataHash"`
		URL      string `json:"url"`
	} `json:"anchor"`
}

type drepThresholds struct {
	CommitteeNoConfidence json.Number `json:"committeeNoConfidence"`
	CommitteeNormal       json.Number `json:"committeeNormal"`
	HardForkInitiation    json.Number `json:"hardForkInitiation"`
	MotionNoConfidence    json.Number `json:"motionNoConfidence"`
	PPEconomicGroup       json.Number `json:"ppEconomicGroup"`
	PPGovGroup            json.Number `json:"ppGovGroup"`
	PPNetworkGroup        json.Number `json:"ppNetworkGroup"`
	PPTechnicalGroup      json.Number `json:"ppTechnicalGroup"`
	TreasuryWithdrawal    json.Number `json:"treasuryWithdrawal"`
	UpdateToConstitution  json.Number `json:"updateToConstitution"`
}

type poolThresholds struct {
	CommitteeNoConfidence json.Number `json:"committeeNoConfidence"`
	CommitteeNormal       json.Number `json:"committeeNormal"`
	HardForkInitiation    json.Number `json:"hardForkInitiation"`
	MotionNoConfidence    json.Number `json:"motionNoConfidence"`
	PPSecurityGroup       json.Number `json:"ppSecurityGroup"`
}

// renderConway gives the committee members' cold credentials from the
// keystore and a 2/3 threshold. Hot keys are authorised later by
// transaction, as on any network. The minimum committee size is the
// member count. There are no initial DReps or delegations. The PlutusV3
// cost model is the full one's first conwayV3Len parameters, the count the
// ledger requires here; renderAlonzo's extraConfig carries the rest.
func renderConway(s Spec, k *keys.Store) ([]byte, error) {
	ms, err := CostModels(s)
	if err != nil {
		return nil, err
	}
	v3 := ms[uint(PlutusV3)]
	v3 = v3[:min(len(v3), conwayV3Len)]
	members := make(map[string]int, s.Committee)
	for i := range s.Committee {
		cold, _ := k.Committee(uint32(i))
		members["keyHash-"+hex.EncodeToString(cold[:])] = committeeExpiry
	}
	d := conwayDoc{
		CommitteeMaxTerm:    committeeExpiry,
		CommitteeMinSize:    s.Committee,
		DRepActivity:        20,
		DRepDeposit:         2_000_000,
		GovActionDeposit:    1_000_000_000,
		GovActionLifetime:   6,
		MinFeeRefScriptCost: "15",
		PlutusV3CostModel:   v3,
		DRepThresholds: drepThresholds{
			CommitteeNoConfidence: "0.6", CommitteeNormal: "0.67", HardForkInitiation: "0.6",
			MotionNoConfidence: "0.67", PPEconomicGroup: "0.67", PPGovGroup: "0.75",
			PPNetworkGroup: "0.67", PPTechnicalGroup: "0.67", TreasuryWithdrawal: "0.67",
			UpdateToConstitution: "0.75",
		},
		PoolThresholds: poolThresholds{
			CommitteeNoConfidence: "0.51", CommitteeNormal: "0.51", HardForkInitiation: "0.51",
			MotionNoConfidence: "0.51", PPSecurityGroup: "0.51",
		},
	}
	d.CommitteeDoc.Members = members
	d.CommitteeDoc.Threshold.Numerator, d.CommitteeDoc.Threshold.Denominator = 2, 3
	d.Constitution.Anchor.DataHash = "0000000000000000000000000000000000000000000000000000000000000000"
	return marshal(d)
}
