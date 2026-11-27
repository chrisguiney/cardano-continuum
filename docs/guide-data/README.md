# docs/guide-data

One JSON file per network (`mainnet.json`, `preprod.json`, `preview.json`)
holding a fit for the guide pages. Top-level keys: `version`, `header`,
`bic`, `clusters`, `units` (a stratified sample of real units) and `actors`.

Written by `lab/export_guide_data.py` from `fit.py --clusters` output; the
output is byte-identical for the same inputs. See
[`lab/README.md`](../../lab/README.md) for the command.
