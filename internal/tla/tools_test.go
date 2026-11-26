package tla

import (
	"os"
	"path/filepath"
	"testing"
)

// testTools returns the repository's TLC wrapper and the 1.8.0 jar, or skips
// the test when `make models-install` has not fetched the jar.
func testTools(t *testing.T) Tools {
	t.Helper()
	jar, err := filepath.Abs("../../models/.tools/tla2tools-1.8.0.jar")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(jar); err != nil {
		t.Skipf("TLC 1.8.0 jar not installed (run make models-install): %v", err)
	}
	tlc, err := filepath.Abs("../../models/bin/tlc")
	if err != nil {
		t.Fatal(err)
	}
	return Tools{TLC: tlc, Jar: jar, Tmp: t.TempDir()}
}

func helloDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.Abs("../../models/specs/hello")
	if err != nil {
		t.Fatal(err)
	}
	return d
}
