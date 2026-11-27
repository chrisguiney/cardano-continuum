package main

import "testing"

func TestParseWatchNamespace(t *testing.T) {
	o, err := parse([]string{"--watch-namespace=lab"})
	if err != nil {
		t.Fatal(err)
	}
	if o.namespace != "lab" {
		t.Fatalf("namespace = %q, want lab", o.namespace)
	}

	o, err = parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.namespace != "" {
		t.Fatalf("default namespace = %q, want every namespace", o.namespace)
	}
}
