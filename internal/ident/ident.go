// Package ident is the single source of organisation-specific identifiers.
// Nothing else in the tree may repeat these literals; a transfer to another
// organisation changes this package, the Makefile variables that mirror it,
// and the import paths.
package ident

const (
	// Module is the Go module path.
	Module = "github.com/chrisguiney/cardano-continuum"

	// ImageRepo is the registry prefix for release images.
	ImageRepo = "ghcr.io/chrisguiney"

	// APIGroup is the Kubernetes API group of the CRDs.
	APIGroup = "continuum.blinklabs.io"

	// LabelPrefix prefixes the Kubernetes labels and annotations this project sets.
	LabelPrefix = "continuum.blinklabs.io"

	// MetricsNS is the Prometheus metric name prefix, without the trailing underscore.
	MetricsNS = "continuum"

	// UpstreamModuleOrg prefixes the Go module paths of the dependencies an
	// overlay build swaps into dingo.
	UpstreamModuleOrg = "github.com/blinklabs-io"

	// UpstreamDingoImage is the repository of the published dingo images
	// that overlay builds layer over.
	UpstreamDingoImage = "ghcr.io/blinklabs-io/dingo"
)
