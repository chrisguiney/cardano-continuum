package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var updateExplorer = flag.Bool("update-explorer", false, "rewrite the explorer golden files")

func seedRunLog(t *testing.T, seed string) string {
	t.Helper()
	dir := t.TempDir()
	var out strings.Builder
	args := []string{"run", "--fake", "--seed", seed, "--epochs", "1", "--out", dir, "--wallets", "20", "--epoch-slots", "60"}
	if err := run(context.Background(), args, &out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	return filepath.Join(dir, "events.log")
}

func explorerGolden(t *testing.T, name string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	path := filepath.Join("testdata", "explorer-"+name+".golden.json")
	if *updateExplorer {
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, b) {
		t.Errorf("%s differs from golden:\n%s", name, b)
	}
}

// TestServeExplorerGoldens serves a seed-7 fake run and compares the
// explorer's answers with goldens: the block list, a block, a transaction
// with an actor behind it, the address it pays and the epoch.
func TestServeExplorerGoldens(t *testing.T) {
	log := seedRunLog(t, "7")
	srv, stop := startServe(t, "--log", log, "--epoch-slots", "60")
	defer stop()

	var blocks map[string]any
	if code := srv.getJSON(t, "/api/explorer/blocks?limit=3", &blocks); code != 200 || len(blocks["rows"].([]any)) != 3 {
		t.Fatalf("blocks %d %+v", code, blocks)
	}
	explorerGolden(t, "blocks", blocks)
	total := int(blocks["total"].(float64))

	// The first block with a transaction, found from the oldest page.
	var all struct {
		Rows []struct {
			Hash string
			Txs  int
		}
	}
	srv.getJSON(t, "/api/explorer/blocks?limit=100&offset="+strconv.Itoa(max(total-100, 0)), &all)
	var withTx string
	for i := len(all.Rows) - 1; i >= 0 && withTx == ""; i-- {
		if all.Rows[i].Txs > 0 {
			withTx = all.Rows[i].Hash
		}
	}
	if withTx == "" {
		t.Fatal("seed-7 run has no block with a transaction")
	}
	var blk struct {
		Txs []struct{ Hash string }
	}
	var blkAny map[string]any
	srv.getJSON(t, "/api/explorer/blocks/"+withTx, &blkAny)
	explorerGolden(t, "block", blkAny)
	b, _ := json.Marshal(blkAny)
	_ = json.Unmarshal(b, &blk)

	var txAny map[string]any
	if code := srv.getJSON(t, "/api/explorer/txs/"+blk.Txs[0].Hash, &txAny); code != 200 {
		t.Fatalf("tx %d", code)
	}
	explorerGolden(t, "tx", txAny)
	if txAny["continuum"] == nil {
		t.Errorf("the run's own transaction has no continuum context: %v", txAny)
	}

	outs := txAny["outputs"].([]any)
	addr := outs[0].(map[string]any)["address"].(string)
	var addrAny map[string]any
	if code := srv.getJSON(t, "/api/explorer/addresses/"+url.PathEscape(addr), &addrAny); code != 200 {
		t.Fatalf("address %d", code)
	}
	explorerGolden(t, "address", addrAny)

	var ep map[string]any
	if code := srv.getJSON(t, "/api/explorer/epochs/0", &ep); code != 200 {
		t.Fatalf("epoch %d", code)
	}
	explorerGolden(t, "epoch", ep)

	var sr struct{ Kind, Route string }
	if code := srv.getJSON(t, "/api/explorer/search?q="+blk.Txs[0].Hash, &sr); code != 200 || sr.Kind != "tx" {
		t.Fatalf("search %d %+v", code, sr)
	}
}
