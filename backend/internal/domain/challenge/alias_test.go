package challenge

import "github.com/stone7890/leash/internal/domain/network"

// networkAlias keeps the conformance test readable without importing the network package into
// every table.
type networkAlias = network.Network
