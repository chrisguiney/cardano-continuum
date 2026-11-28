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

// The missing witness may be one of several expected failures: the
// decoder's Unknown stands for it whatever else was expected.
func TestKnownIssueMatchesMissingWitnessAmongExpected(t *testing.T) {
	t.Parallel()
	f := unclassified("reject naming only MissingVKeyWitnessesUTXOW,OutsideValidityIntervalUTxO", "rejected Unknown")
	if k, ok := KnownIssueOf(f); !ok || k.Ref != "gouroboros#3347" {
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
		"no facts":              {Kind: Unclassified},
		"accept or reject":      unclassified("accept, or reject naming only MissingVKeyWitnessesUTXOW", "rejected Unknown"),
		"class prefix":          unclassified("reject naming only XMissingVKeyWitnessesUTXOW", "rejected Unknown"),
		"unknown beside others": unclassified("reject naming only MissingVKeyWitnessesUTXOW", "rejected BadInputsUTxO,Unknown"),
	} {
		if k, ok := KnownIssueOf(f); ok {
			t.Errorf("%s matched %s", name, k.Ref)
		}
	}
}
