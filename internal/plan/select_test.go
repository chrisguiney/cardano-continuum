package plan

import (
	"math/rand/v2"
	"slices"
	"testing"
)

func TestSelect(t *testing.T) {
	a := Utxo{Ref: ref(1), Lovelace: 5}
	b := Utxo{Ref: ref(2), Lovelace: 9}
	c := Utxo{Ref: ref(3), Lovelace: 5}
	cases := []struct {
		name  string
		avail []Utxo
		need  uint64
		want  []Utxo
	}{
		{"largest first", []Utxo{a, b, c}, 9, []Utxo{b}},
		{"ties by ref", []Utxo{c, b, a}, 12, []Utxo{b, a}},
		{"all", []Utxo{a, b, c}, 19, []Utxo{b, a, c}},
		{"too little", []Utxo{a, b, c}, 20, nil},
		{"nothing", nil, 1, nil},
	}
	for _, tc := range cases {
		if got := Select(tc.avail, tc.need); !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSelectIgnoresOrder(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	var avail []Utxo
	for i := range 40 {
		avail = append(avail, Utxo{Ref: ref(uint64(i)), Lovelace: uint64(1 + r.IntN(5))})
	}
	want := Select(avail, 50)
	for range 20 {
		r.Shuffle(len(avail), func(i, j int) { avail[i], avail[j] = avail[j], avail[i] })
		in := slices.Clone(avail)
		if got := Select(avail, 50); !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		if !slices.Equal(in, avail) {
			t.Fatal("Select reordered its argument")
		}
	}
}
