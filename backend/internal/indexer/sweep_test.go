package indexer

import (
	"context"
	"testing"

	"github.com/gagliardetto/solana-go"

	"github.com/stone7890/leash/internal/chain"
	"github.com/stone7890/leash/internal/domain/network"
	"github.com/stone7890/leash/internal/store"
)

// refusingAdapter fails every read. Only SignatureStatus is reachable from `look`, and the point
// of the fake is to prove it is NOT reached.
type refusingAdapter struct {
	chain.Adapter
	asked int
}

func (a *refusingAdapter) SignatureStatus(context.Context, network.Network, string) (
	chain.Confirmation, error) {
	a.asked++
	return chain.Confirmation{}, errAsked
}

var errAsked = errAskedType{}

type errAskedType struct{}

func (errAskedType) Error() string { return "the adapter should not have been asked" }

// A signature that does not decode is an unreadable record, not a chain error.
//
// The distinction is load-bearing: an error makes the sweep `continue`, so the payment never
// reaches the 24-hour branch, its reservation is never released, and the allowance loses that
// money for ever to a warning nobody acts on. This is the real 44-character value that sat in
// `signed` doing exactly that — a 32-byte key where a 64-byte signature belongs.
func TestASignatureThatDoesNotDecodeIsNotAChainError(t *testing.T) {
	a := &refusingAdapter{}
	ix := &Indexer{adapter: a}

	conf, err := ix.look(context.Background(), network.Sandbox, store.Payment{
		ID:        "pay_test_01M1TC24HA9ASZCBMYW6QJZBGD",
		Signature: "5KdWbxNiZx5G42SeJrzQhcUTn9kVhzRoW4HwNXUT6Mxu",
	})
	if err != nil {
		t.Fatalf("an undecodable signature must not be reported as a chain error: %v", err)
	}
	if conf.Found {
		t.Fatal("nothing was found, so Found must be false")
	}
	if a.asked != 0 {
		t.Fatalf("asked the chain %d times about a signature that cannot be parsed", a.asked)
	}
}

// The opposite case, so the fix does not quietly swallow a real outage: a well-formed signature
// the RPC could not answer for IS an error, and the sweep must skip rather than guess.
func TestAnRPCFailureOnAGoodSignatureIsStillAnError(t *testing.T) {
	a := &refusingAdapter{}
	ix := &Indexer{adapter: a}

	// 64 zero bytes, which base58 encodes as 64 leading ones — well formed, and certainly never
	// submitted.
	good := solana.Signature{}.String()
	if _, err := ix.look(context.Background(), network.Sandbox, store.Payment{
		ID: "pay_test_good", Signature: good,
	}); err == nil {
		t.Fatal("an RPC failure was swallowed; the sweep would treat an outage as an outcome")
	}
	if a.asked != 1 {
		t.Fatalf("the chain was asked %d times, want 1", a.asked)
	}
}
