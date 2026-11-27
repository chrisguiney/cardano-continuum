// Package publish copies a rendered devnet genesis (genesis.WriteDir's
// layout) into the objects the operator mounts: one ConfigMap with the node
// configuration and genesis files, the network's genesisRef, and one Secret
// per genesis pool with its KES key, VRF key and operational certificate.
// The genesis Job runs it after rendering, so nothing outside the cluster
// copies files off the genesis volume.
package publish
