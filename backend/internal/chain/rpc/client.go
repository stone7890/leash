// Package rpc holds one Solana client per network, with a fallback.
//
// There is deliberately no global client and no global endpoint. Invariant I8: the endpoint is
// chosen from the record being acted on, so a caller must present a network to get a client, and
// there is no function here that returns "the" client.
package rpc

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/gagliardetto/solana-go/rpc"
	"github.com/stone7890/leash/internal/domain/network"
)

// Pool is the set of clients for the networks this process was configured for.
type Pool struct {
	byNetwork map[network.Network]*pair
}

type pair struct {
	net      network.Network
	primary  *rpc.Client
	fallback *rpc.Client
	// usingFallback is read by the health endpoint so the degraded banner can say which endpoint
	// answered. A degraded mode that is invisible is a degraded mode nobody fixes.
	usingFallback atomic.Bool
}

type Endpoints struct {
	Primary  string
	Fallback string
}

func New(endpoints map[network.Network]Endpoints) (*Pool, error) {
	p := &Pool{byNetwork: map[network.Network]*pair{}}
	for n, e := range endpoints {
		if e.Primary == "" {
			return nil, fmt.Errorf("no RPC endpoint configured for %s", n)
		}
		pr := &pair{net: n, primary: rpc.New(e.Primary)}
		if e.Fallback != "" {
			pr.fallback = rpc.New(e.Fallback)
		}
		p.byNetwork[n] = pr
	}
	return p, nil
}

// For returns the client for a network, and refuses when there is none.
//
// It refuses rather than falling back to another network's client — which would be the single
// mistake I8 exists to prevent, and the one a "sensible default" would introduce.
func (p *Pool) For(n network.Network) (*rpc.Client, error) {
	pr, ok := p.byNetwork[n]
	if !ok {
		return nil, fmt.Errorf(
			"no RPC endpoint is configured for %s. This process was started with a different "+
				"set of networks, and there is no default — the endpoint is chosen per record (I8)", n)
	}
	if pr.usingFallback.Load() && pr.fallback != nil {
		return pr.fallback, nil
	}
	return pr.primary, nil
}

// Do runs a call against the primary and moves to the fallback if it fails.
//
// An RPC outage makes data LATE, never wrong: `unknown` is a first-class payment state, and
// nothing is inferred from silence. So failing over is safe, and it is loud.
func Do[T any](ctx context.Context, p *Pool, n network.Network,
	call func(*rpc.Client) (T, error)) (T, error) {
	var zero T
	pr, ok := p.byNetwork[n]
	if !ok {
		return zero, fmt.Errorf("no RPC endpoint is configured for %s", n)
	}

	client := pr.primary
	if pr.usingFallback.Load() && pr.fallback != nil {
		client = pr.fallback
	}
	out, err := call(client)
	if err == nil {
		return out, nil
	}
	if pr.fallback == nil || pr.usingFallback.Load() {
		return zero, err
	}

	slog.Error("the primary RPC failed — moving to the fallback",
		"network", n, "err", err)
	pr.usingFallback.Store(true)
	return call(pr.fallback)
}

// UsingFallback reports whether a network has failed over, for the degraded banner.
func (p *Pool) UsingFallback(n network.Network) bool {
	pr, ok := p.byNetwork[n]
	return ok && pr.usingFallback.Load()
}

// Networks lists what this process was configured for.
func (p *Pool) Networks() []network.Network {
	out := make([]network.Network, 0, len(p.byNetwork))
	for n := range p.byNetwork {
		out = append(out, n)
	}
	return out
}
