package sink

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ManifestName is the manifest file in the export directory. It is replaced
// atomically after every export, last, so it names exactly the committed
// part files.
const ManifestName = "_continuum_events.json"

// File is one committed part file.
type File struct {
	Name     string `json:"name"`
	Rows     int64  `json:"rows"`
	FirstSeq uint64 `json:"first_seq"`
	LastSeq  uint64 `json:"last_seq"`
}

// Manifest lists the committed files of each table and the highest log seq
// they cover.
type Manifest struct {
	SchemaVersion int               `json:"schema_version"`
	LastSeq       uint64            `json:"last_seq"`
	Header        string            `json:"header_fingerprint"`
	Tables        map[string][]File `json:"tables"`
}

// readManifest returns the manifest in dir, or a zero one if there is none.
func readManifest(dir string) (Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if errors.Is(err, fs.ErrNotExist) {
		return Manifest{SchemaVersion: SchemaVersion, Tables: map[string][]File{}}, nil
	}
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse %s: %w", ManifestName, err)
	}
	if m.SchemaVersion != SchemaVersion {
		return Manifest{}, fmt.Errorf("%s has schema version %d, this build writes %d; export into a new directory",
			ManifestName, m.SchemaVersion, SchemaVersion)
	}
	if m.Tables == nil {
		m.Tables = map[string][]File{}
	}
	return m, nil
}

// writeManifest replaces the manifest atomically: a synced temporary file,
// the fault hook, then a rename.
func writeManifest(dir string, m Manifest, fault func(string) error) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := filepath.Join(dir, tmpPrefix+ManifestName)
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := bytes.NewReader(b).WriteTo(f); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := hook(fault, StageManifestTmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, ManifestName)); err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	return errors.Join(err, d.Close())
}

func hook(fault func(string) error, stage string) error {
	if fault == nil {
		return nil
	}
	return fault(stage)
}

// cleanOrphans removes temporary files and part files the manifest does not
// list: what a crash between writing a part and replacing the manifest
// leaves behind.
func cleanOrphans(dir string, m Manifest) error {
	for _, name := range TableNames {
		listed := map[string]bool{}
		for _, f := range m.Tables[name] {
			listed[f.Name] = true
		}
		ents, err := os.ReadDir(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		for _, e := range ents {
			if listed[e.Name()] {
				continue
			}
			if err := os.Remove(filepath.Join(dir, name, e.Name())); err != nil {
				return err
			}
		}
	}
	err := os.Remove(filepath.Join(dir, tmpPrefix+ManifestName))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
