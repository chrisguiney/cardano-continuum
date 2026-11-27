package oracle

import "testing"

func unclassified(expected, verdict string) Finding {
	return Finding{Kind: Unclassified, Facts: []Fact{
		key("verdict", verdict), note("expected", expected), note("detail", "x")}}
}

func TestKnownIssueMatchesMissingWitnessDecodedUnknown(t *testing.T) {
	t.Parallel()
	f := unclassified("reject naming only MissingVKeyWitnessesUTXOW", "rejected Unknown")
	k, ok := KnownIssueOf(f)
	if !ok || k.Ref != "gouroboros#3347" {
		t.Fatalf("got %+v %v", k, ok)
	}
}

func TestKnownIssueDoesNotSwallowOtherShapes(t *testing.T) {
	t.Parallel()
	for name, f := range map[string]Finding{
		"other expected class": unclassified("reject naming only BadInputsUTxO", "rejected Unknown"),
		"other verdict":        unclassified("reject naming only MissingVKeyWitnessesUTXOW", "rejected FeeTooSmall"),
		"other kind": {Kind: WrongClass, Facts: []Fact{
			key("verdict", "rejected Unknown"), note("expected", "reject naming only MissingVKeyWitnessesUTXOW")}},
		"no facts": {Kind: Unclassified},
	} {
		if k, ok := KnownIssueOf(f); ok {
			t.Errorf("%s matched %s", name, k.Ref)
		}
	}
}
