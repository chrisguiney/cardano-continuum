package explorer

import (
	"encoding/hex"
	"reflect"
	"strings"
	"unicode"

	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// certView decodes one certificate. Pure. A certificate type this decoder
// does not name keeps its type's name as the kind, so nothing is hidden.
func certView(c common.Certificate) CertView {
	var val any = c
	if rv := reflect.ValueOf(c); rv.Kind() == reflect.Pointer && !rv.IsNil() {
		val = rv.Elem().Interface()
	}
	v := CertView{Kind: snake(reflect.TypeOf(val).String(), "Certificate")}
	switch x := val.(type) {
	case common.StakeRegistrationCertificate:
		v.Kind, v.Credential = "stake_registration", credHex(x.StakeCredential)
	case common.StakeDeregistrationCertificate:
		v.Kind, v.Credential = "stake_deregistration", credHex(x.StakeCredential)
	case common.StakeDelegationCertificate:
		v.Kind, v.Pool = "stake_delegation", x.PoolKeyHash.String()
		if x.StakeCredential != nil {
			v.Credential = credHex(*x.StakeCredential)
		}
	case common.PoolRegistrationCertificate:
		v.Kind, v.Pool, v.Amount = "pool_registration", x.Operator.String(), x.Pledge
	case common.PoolRetirementCertificate:
		v.Kind, v.Pool, v.Epoch = "pool_retirement", x.PoolKeyHash.String(), x.Epoch
	case common.RegistrationCertificate:
		v.Kind, v.Credential, v.Amount = "stake_registration", credHex(x.StakeCredential), x.Amount
	case common.DeregistrationCertificate:
		v.Kind, v.Credential, v.Amount = "stake_deregistration", credHex(x.StakeCredential), x.Amount
	case common.VoteDelegationCertificate:
		v.Kind, v.Credential, v.DRep = "vote_delegation", credHex(x.StakeCredential), drepText(x.Drep)
	case common.StakeVoteDelegationCertificate:
		v.Kind, v.Credential, v.Pool, v.DRep = "stake_vote_delegation", credHex(x.StakeCredential), x.PoolKeyHash.String(), drepText(x.Drep)
	case common.StakeRegistrationDelegationCertificate:
		v.Kind, v.Credential, v.Pool, v.Amount = "stake_registration_delegation", credHex(x.StakeCredential), x.PoolKeyHash.String(), x.Amount
	case common.VoteRegistrationDelegationCertificate:
		v.Kind, v.Credential, v.DRep, v.Amount = "vote_registration_delegation", credHex(x.StakeCredential), drepText(x.Drep), x.Amount
	case common.StakeVoteRegistrationDelegationCertificate:
		v.Kind, v.Credential, v.Pool, v.DRep, v.Amount = "stake_vote_registration_delegation", credHex(x.StakeCredential), x.PoolKeyHash.String(), drepText(x.Drep), x.Amount
	case common.RegistrationDrepCertificate:
		v.Kind, v.Credential, v.Amount, v.Anchor = "drep_registration", credHex(x.DrepCredential), x.Amount, anchorText(x.Anchor)
	case common.DeregistrationDrepCertificate:
		v.Kind, v.Credential, v.Amount = "drep_deregistration", credHex(x.DrepCredential), x.Amount
	case common.UpdateDrepCertificate:
		v.Kind, v.Credential, v.Anchor = "drep_update", credHex(x.DrepCredential), anchorText(x.Anchor)
	case common.AuthCommitteeHotCertificate:
		v.Kind, v.Credential, v.DRep = "committee_hot_auth", credHex(x.ColdCredential), credHex(x.HotCredential)
	case common.ResignCommitteeColdCertificate:
		v.Kind, v.Credential, v.Anchor = "committee_resign", credHex(x.ColdCredential), anchorText(x.Anchor)
	}
	return v
}

func credHex(c common.Credential) string { return c.Credential.String() }

func drepText(d common.Drep) string {
	switch d.Type {
	case common.DrepTypeAddrKeyHash:
		return "key:" + hex.EncodeToString(d.Credential)
	case common.DrepTypeScriptHash:
		return "script:" + hex.EncodeToString(d.Credential)
	case common.DrepTypeAbstain:
		return "always_abstain"
	case common.DrepTypeNoConfidence:
		return "always_no_confidence"
	}
	return "unknown"
}

func anchorText(a *common.GovAnchor) string {
	if a == nil {
		return ""
	}
	return a.Url
}

// snake turns a Go type name like "common.PoolRetirementCertificate" into
// "pool_retirement" by dropping the package and the suffix.
func snake(typeName, suffix string) string {
	if i := strings.LastIndex(typeName, "."); i >= 0 {
		typeName = typeName[i+1:]
	}
	typeName = strings.TrimSuffix(typeName, suffix)
	var b strings.Builder
	for i, r := range typeName {
		if unicode.IsUpper(r) && i > 0 {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}
