package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// The sandbox has no pre-existing USDC, so `leashctl bootstrap` creates one and writes what it made
// to a shared volume. Reading it here is what lets `make start` work on a fresh machine with no
// manual step: nobody has to copy a mint address into an environment file.
//
// It applies to the SANDBOX ONLY. A mainnet mint is a real, published address and must be
// configured explicitly — discovering it from a file a process happened to find would be exactly
// the kind of convenience that invariant I8 exists to refuse.
type sandboxState struct {
	Network      string `json:"network"`
	Mint         string `json:"usdc_mint"`
	Demo402PayTo string `json:"demo402_pay_to"`
	FaucetWallet string `json:"faucet_wallet"`
}

func readSandboxState() (sandboxState, bool) {
	dir := os.Getenv("LEASH_STATE_DIR")
	if dir == "" {
		dir = "/state"
	}
	b, err := os.ReadFile(filepath.Join(dir, "sandbox.json"))
	if err != nil {
		return sandboxState{}, false
	}
	var s sandboxState
	if err := json.Unmarshal(b, &s); err != nil {
		return sandboxState{}, false
	}
	if s.Network != "sandbox" || s.Mint == "" {
		return sandboxState{}, false
	}
	return s, true
}
