package genesis

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// markerFile holds the SpecHash of the bundle in a directory. It is written
// last, so its presence means the directory is complete.
const markerFile = "spec.sha256"

// WriteDir writes b under dir as
//
//	config/{config.json, *-genesis.json}
//	pools/<i>/{topology.json, kes.skey, vrf.skey, opcert.cert, cold.vkey}
//	spec.sha256
//
// If dir already holds a complete bundle for the same SpecHash, WriteDir
// changes nothing and reports false: rerunning keeps the original
// systemStart. A complete bundle for a different spec is an error. Secret
// files are mode 0600.
func WriteDir(dir string, b Bundle) (bool, error) {
	prev, err := os.ReadFile(filepath.Join(dir, markerFile))
	switch {
	case err == nil:
		if got := strings.TrimSpace(string(prev)); got != b.SpecHash {
			return false, fmt.Errorf("%s holds a devnet for spec %s, not %s", dir, got, b.SpecHash)
		}
		return false, nil
	case !errors.Is(err, fs.ErrNotExist):
		return false, fmt.Errorf("read %s marker: %w", dir, err)
	}

	cfg := filepath.Join(dir, "config")
	files := map[string][]byte{
		ByronFile: b.Byron, ShelleyFile: b.Shelley, AlonzoFile: b.Alonzo,
		ConwayFile: b.Conway, ConfigFile: b.NodeConfig,
	}
	if err := writeFiles(cfg, files, 0o644); err != nil {
		return false, err
	}
	for i, p := range b.Pools {
		pd := filepath.Join(dir, "pools", strconv.Itoa(i))
		if err := writeFiles(pd, map[string][]byte{"topology.json": p.Topology}, 0o644); err != nil {
			return false, err
		}
		if err := writeFiles(pd, p.Files, 0o600); err != nil {
			return false, err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, markerFile), []byte(b.SpecHash+"\n"), 0o644); err != nil {
		return false, fmt.Errorf("write %s marker: %w", dir, err)
	}
	return true, nil
}

func writeFiles(dir string, files map[string][]byte, mode os.FileMode) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	for name, data := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, mode); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		// WriteFile keeps the mode of an existing file.
		if err := os.Chmod(path, mode); err != nil {
			return fmt.Errorf("chmod %s: %w", path, err)
		}
	}
	return nil
}
