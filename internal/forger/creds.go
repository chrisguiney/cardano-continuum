package forger

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/kes"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/vrf"
)

// Entries of the keys directory. The first three are what every block
// producer mounts (internal/operator's keys Secret). A scenario adds
// alternate certificates: the forger holds no cold key, so the engine
// issues each one a misbehaviour needs.
const (
	KESFile  = "kes.skey"
	VRFFile  = "vrf.skey"
	CertFile = "opcert.cert"
	// BehaviourFile is the scenario's Schedule; without it the forger is
	// honest.
	BehaviourFile = "behaviour.json"
)

// CertName names an operational certificate in the keys directory. All of
// them bind the same KES key.
type CertName string

// Certificates.
const (
	// CertPrimary is opcert.cert, the one an honest node would run with.
	CertPrimary CertName = ""
	// CertRewind has a counter below the pool's counter on chain.
	CertRewind CertName = "rewind"
	// CertLeap has a counter more than one above the pool's counter on chain.
	CertLeap CertName = "leap"
	// CertFuture starts at a KES period that has not begun.
	CertFuture CertName = "future"
)

func certFile(n CertName) string {
	if n == CertPrimary {
		return CertFile
	}
	return "opcert-" + string(n) + ".cert"
}

// OpCert is an operational certificate: the cold key's signature over a KES
// key, a counter and the KES period the key starts at.
type OpCert struct {
	KESVKey  []byte
	Counter  uint64
	Start    uint64
	Sig      []byte
	ColdVKey []byte
}

// ParseOpCert decodes the cardano-cli certificate CBOR
// [[kes_vkey, counter, kes_period, cold_sig], cold_vkey].
func ParseOpCert(raw []byte) (OpCert, error) {
	var doc struct {
		cbor.StructAsArray
		Cert struct {
			cbor.StructAsArray
			KESVKey []byte
			Counter uint64
			Start   uint64
			Sig     []byte
		}
		ColdVKey []byte
	}
	if _, err := cbor.Decode(raw, &doc); err != nil {
		return OpCert{}, fmt.Errorf("decode operational certificate: %w", err)
	}
	c := OpCert{KESVKey: doc.Cert.KESVKey, Counter: doc.Cert.Counter, Start: doc.Cert.Start, Sig: doc.Cert.Sig, ColdVKey: doc.ColdVKey}
	if len(c.KESVKey) != 32 || len(c.ColdVKey) != 32 || len(c.Sig) != 64 {
		return OpCert{}, fmt.Errorf("operational certificate has a %d-byte KES key, %d-byte cold key and %d-byte signature",
			len(c.KESVKey), len(c.ColdVKey), len(c.Sig))
	}
	return c, nil
}

// Credentials are a pool's hot keys and its operational certificates. The
// secret parts never leave the type; it renders redacted.
type Credentials struct {
	vrfSeed []byte
	// kesKey is the KES signing key at evolution 0.
	kesKey []byte

	VRFVKey  []byte
	KESVKey  []byte
	ColdVKey []byte
	PoolID   [28]byte
	Certs    map[CertName]OpCert
}

func (c *Credentials) redacted() string {
	return fmt.Sprintf("Credentials(pool %x, redacted)", c.PoolID[:4])
}

func (c *Credentials) String() string             { return c.redacted() }
func (c *Credentials) GoString() string           { return c.redacted() }
func (c *Credentials) Format(f fmt.State, _ rune) { _, _ = fmt.Fprint(f, c.redacted()) }
func (c *Credentials) MarshalJSON() ([]byte, error) {
	return json.Marshal(c.redacted())
}
func (c *Credentials) LogValue() slog.Value { return slog.StringValue(c.redacted()) }

// maxKeyFile bounds a key file read; the largest, a KES key envelope, is
// under 2 KiB.
const maxKeyFile = 16 << 10

// LoadCredentials reads the keys directory. Alternate certificates and the
// behaviour file are optional.
func LoadCredentials(dir string) (*Credentials, error) {
	names := []string{KESFile, VRFFile, CertFile}
	for _, n := range []CertName{CertRewind, CertLeap, CertFuture} {
		names = append(names, certFile(n))
	}
	files := map[string][]byte{}
	for i, name := range names {
		raw, err := readBounded(filepath.Join(dir, name), maxKeyFile)
		if errors.Is(err, os.ErrNotExist) && i >= 3 {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read key file %s: %w", name, err)
		}
		files[name] = raw
	}
	return ParseCredentials(files)
}

func readBounded(path string, limit int64) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() > limit {
		return nil, fmt.Errorf("%d bytes, over the %d byte limit", st.Size(), limit)
	}
	return os.ReadFile(path)
}

// ParseCredentials builds Credentials from text envelopes keyed by file
// name.
func ParseCredentials(files map[string][]byte) (*Credentials, error) {
	c := &Credentials{Certs: map[CertName]OpCert{}}
	vrfKey, err := envelopeBytes(files, VRFFile)
	if err != nil {
		return nil, err
	}
	if len(vrfKey) != vrf.SeedSize && len(vrfKey) != vrf.SeedSize+vrf.PublicKeySize {
		return nil, fmt.Errorf("%s holds %d bytes, want %d or %d", VRFFile, len(vrfKey), vrf.SeedSize, vrf.SeedSize+vrf.PublicKeySize)
	}
	c.vrfSeed = vrfKey[:vrf.SeedSize]
	if c.VRFVKey, _, err = vrf.KeyGen(c.vrfSeed); err != nil {
		return nil, fmt.Errorf("derive VRF key: %w", err)
	}
	if c.kesKey, err = envelopeBytes(files, KESFile); err != nil {
		return nil, err
	}
	if len(c.kesKey) != kes.CardanoKesSecretKeySize {
		return nil, fmt.Errorf("%s holds %d bytes, want %d", KESFile, len(c.kesKey), kes.CardanoKesSecretKeySize)
	}
	c.KESVKey = kes.PublicKey(&kes.SecretKey{Depth: kes.CardanoKesDepth, Data: c.kesKey})
	for name := range files {
		if err := c.addCert(files, name); err != nil {
			return nil, err
		}
	}
	primary, ok := c.Certs[CertPrimary]
	if !ok {
		return nil, fmt.Errorf("no %s", CertFile)
	}
	c.ColdVKey = primary.ColdVKey
	c.PoolID = common.Blake2b224Hash(c.ColdVKey)
	return c, nil
}

func (c *Credentials) addCert(files map[string][]byte, file string) error {
	name, ok := certName(file)
	if !ok {
		return nil
	}
	raw, err := envelopeBytesRaw(files, file)
	if err != nil {
		return err
	}
	oc, err := ParseOpCert(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	if string(oc.KESVKey) != string(c.KESVKey) {
		return fmt.Errorf("%s certifies another KES key than %s", file, KESFile)
	}
	c.Certs[name] = oc
	return nil
}

func certName(file string) (CertName, bool) {
	if file == CertFile {
		return CertPrimary, true
	}
	n, ok := strings.CutPrefix(file, "opcert-")
	if !ok {
		return "", false
	}
	n, ok = strings.CutSuffix(n, ".cert")
	return CertName(n), ok && n != ""
}

// envelopeBytesRaw returns the CBOR payload of the text envelope files[name].
func envelopeBytesRaw(files map[string][]byte, name string) ([]byte, error) {
	raw, ok := files[name]
	if !ok {
		return nil, fmt.Errorf("no %s", name)
	}
	var env struct {
		CborHex string `json:"cborHex"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("%s is not a text envelope", name)
	}
	out, err := hex.DecodeString(env.CborHex)
	if err != nil || len(out) == 0 {
		return nil, fmt.Errorf("%s has no hex CBOR payload", name)
	}
	return out, nil
}

// envelopeBytes returns the byte string a key envelope's CBOR wraps.
func envelopeBytes(files map[string][]byte, name string) ([]byte, error) {
	raw, err := envelopeBytesRaw(files, name)
	if err != nil {
		return nil, err
	}
	var out []byte
	if _, err := cbor.Decode(raw, &out); err != nil {
		return nil, fmt.Errorf("%s payload is not a CBOR byte string", name)
	}
	return out, nil
}

// vrfProve proves alpha with the pool's VRF key.
func (c *Credentials) vrfProve(alpha []byte) (proof, output []byte, err error) {
	return vrf.Prove(c.vrfSeed, alpha)
}

// kesSign signs msg with the KES key evolved to evolution, the number of
// periods since the certificate's start.
func (c *Credentials) kesSign(evolution uint64, msg []byte) ([]byte, error) {
	if evolution >= kes.MaxPeriod(kes.CardanoKesDepth) {
		return nil, fmt.Errorf("KES evolution %d is past the key's last period", evolution)
	}
	// Update erases its input, so each signature evolves a private copy.
	sk := &kes.SecretKey{Depth: kes.CardanoKesDepth, Data: append([]byte(nil), c.kesKey...)}
	for sk.Period < evolution {
		next, err := kes.Update(sk)
		if err != nil {
			return nil, fmt.Errorf("evolve KES key to %d: %w", evolution, err)
		}
		sk = next
	}
	sig, err := kes.Sign(sk, evolution, msg)
	sk.Zeroize()
	if err != nil {
		return nil, fmt.Errorf("KES sign at evolution %d: %w", evolution, err)
	}
	return sig, nil
}
