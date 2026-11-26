package fakechain

import (
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

// certTx spends w's genesis output with certs in the body, paying deposit
// into the deposit pot and the rest back to w.
func (f *fixture) certTx(w keys.Wallet, deposit uint64, certs ...any) txSpec {
	s := txSpec{
		ins:      []Ref{f.genesisRef(w)},
		outs:     []out{{w.Address(), 0}},
		fee:      -1,
		signers:  []keys.Wallet{w},
		extra:    []keys.Signer{w.StakeSigner()},
		bodyHook: func(m map[uint]any) { m[4] = certs },
	}
	return s.balanced(f.t, testLovelace-deposit)
}

func stakeCred(w keys.Wallet) []any {
	h := w.StakeKeyHash()
	return []any{0, h[:]}
}

// regCert is a Conway reg_cert: [7, stake_credential, deposit].
func regCert(w keys.Wallet, deposit uint64) []any { return []any{7, stakeCred(w), deposit} }

func keyDeposit() uint64 { return uint64(DefaultParams().KeyDeposit) }

func TestStakeRegistrationRecorded(t *testing.T) {
	f := newFixture(t, nil)
	id := f.mustSubmit(f.certTx(f.alice, keyDeposit(), regCert(f.alice, keyDeposit())).build(t))
	if got := f.chain.Deposits(); got != 0 {
		t.Fatalf("deposits %d before the block", got)
	}
	f.advance(2)
	if got := f.chain.Deposits(); got != keyDeposit() {
		t.Fatalf("deposits %d, want %d", got, keyDeposit())
	}
	if !f.chain.StakeRegistered(common.Credential{CredType: 0, Credential: common.Blake2b224(f.alice.StakeKeyHash())}) {
		t.Fatal("registration not recorded")
	}
	// Registering again from the change output fails as the ledger does.
	again := txSpec{
		ins: []Ref{{TxID: id, Index: 0}}, outs: []out{{f.alice.Address(), 0}}, fee: -1,
		signers: []keys.Wallet{f.alice}, extra: []keys.Signer{f.alice.StakeSigner()},
		bodyHook: func(m map[uint]any) { m[4] = []any{regCert(f.alice, keyDeposit())} },
	}
	change, _ := f.chain.Utxo(Ref{TxID: id, Index: 0})
	again = again.balanced(t, change.Output.Amount().Uint64()-keyDeposit())
	if cs := rejectClasses(t, f.submit(again.build(t))); !hasClass(cs, StakeKeyRegistered) {
		t.Fatalf("classes %v, want %s", cs, StakeKeyRegistered)
	}
}

func TestCertificateFailureClasses(t *testing.T) {
	var unknownPool [28]byte
	unknownPool[0] = 9
	tests := []struct {
		name    string
		deposit uint64
		certs   func(f *fixture) []any
		want    Class
	}{
		{"wrong deposit", keyDeposit() + 1, func(f *fixture) []any {
			return []any{regCert(f.alice, keyDeposit()+1)}
		}, IncorrectDeposit},
		{"vote-delegate unregistered", 0, func(f *fixture) []any {
			return []any{[]any{9, stakeCred(f.alice), []any{2}}}
		}, StakeKeyNotRegistered},
		{"delegate to unknown pool", keyDeposit(), func(f *fixture) []any {
			return []any{regCert(f.alice, keyDeposit()), []any{2, stakeCred(f.alice), unknownPool[:]}}
		}, DelegateeNotRegistered},
		{"vote-delegate to unknown drep", keyDeposit(), func(f *fixture) []any {
			return []any{regCert(f.alice, keyDeposit()), []any{9, stakeCred(f.alice), []any{0, unknownPool[:]}}}
		}, DelegateeDRepNotRegistered},
		{"deregister unknown drep", 0, func(f *fixture) []any {
			return []any{[]any{17, []any{0, unknownPool[:]}, uint64(DefaultParams().DRepDeposit)}}
		}, DRepNotRegistered},
		{"retire unknown pool", 0, func(f *fixture) []any {
			return []any{[]any{4, unknownPool[:], uint64(1)}}
		}, PoolNotRegistered},
		{"hot key for a non-member", 0, func(f *fixture) []any {
			return []any{[]any{14, []any{0, unknownPool[:]}, []any{0, unknownPool[:]}}}
		}, CommitteeIsUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, nil)
			tx := f.certTx(f.alice, tt.deposit, tt.certs(f)...).build(t)
			if cs := rejectClasses(t, f.submit(tx)); !hasClass(cs, tt.want) {
				t.Fatalf("classes %v, want %s", cs, tt.want)
			}
		})
	}
}

func TestRollbackRestoresCertState(t *testing.T) {
	f := newFixture(t, nil)
	tx := f.certTx(f.alice, keyDeposit(), regCert(f.alice, keyDeposit())).build(t)
	f.mustSubmit(tx)
	f.advance(2)
	if err := f.chain.Inject(Rollback{Depth: 1}); err != nil {
		t.Fatal(err)
	}
	if got := f.chain.Deposits(); got != 0 {
		t.Fatalf("deposits %d after rollback", got)
	}
	// The rolled-back registration applies again: the credential is free.
	f.mustSubmit(tx)
}

func TestMempoolSeesEarlierCertificates(t *testing.T) {
	f := newFixture(t, nil)
	reg := f.certTx(f.alice, keyDeposit(), regCert(f.alice, keyDeposit()))
	id := f.mustSubmit(reg.build(t))
	// Deregistering while the registration is still in the mempool proves
	// the mempool validates against the certificates before it.
	s := txSpec{
		ins: []Ref{{TxID: id, Index: 0}}, outs: []out{{f.alice.Address(), 0}}, fee: -1,
		signers: []keys.Wallet{f.alice}, extra: []keys.Signer{f.alice.StakeSigner()},
		bodyHook: func(m map[uint]any) { m[4] = []any{[]any{8, stakeCred(f.alice), keyDeposit()}} },
	}
	s = s.balanced(t, reg.outs[0].coin+keyDeposit())
	f.mustSubmit(s.build(t))
	f.advance(2)
	if got := f.chain.Deposits(); got != 0 {
		t.Fatalf("deposits %d after register and deregister", got)
	}
}

// TestGenesisStaking: a genesis pool is registered and its delegators'
// credentials are registered and delegated to it, with no deposit held.
func TestGenesisStaking(t *testing.T) {
	op := common.PoolKeyHash{9}
	f := newFixture(t, func(c *Config) {
		c.Staking = []GenesisStake{{
			Pool:       common.PoolRegistrationCertificate{Operator: op},
			Delegators: []common.Blake2b224{common.Blake2b224(c.Genesis[1].Address.StakeKeyHash())},
		}}
	})
	l := f.chain.Ledger()
	if len(l.Pools) != 1 || l.Pools[0].ID != common.Blake2b224(op) {
		t.Fatalf("pools %+v", l.Pools)
	}
	if len(l.Accounts) != 1 || l.Accounts[0].Pool == nil || *l.Accounts[0].Pool != common.Blake2b224(op) {
		t.Fatalf("accounts %+v", l.Accounts)
	}
	if !f.chain.StakeRegistered(common.Credential{Credential: common.Blake2b224(f.bob.StakeKeyHash())}) {
		t.Fatal("delegator not registered")
	}
	if f.chain.Deposits() != 0 {
		t.Fatalf("deposits %d", f.chain.Deposits())
	}
}
