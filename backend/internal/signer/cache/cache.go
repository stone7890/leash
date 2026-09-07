// Package cache holds what the signing path reads from memory rather than from the network.
//
// The latency budget is unachievable with a round trip per lookup, so three things are cached, each
// with a TTL chosen for a stated reason — and each with an eviction path that does not wait for it.
package cache

import (
	"context"
	"crypto/ed25519"
	"log/slog"
	"sync"
	"time"

	"github.com/stone7890/leash/internal/domain/network"
	"github.com/stone7890/leash/internal/store"
)

// Snapshots caches the agent, its policy and its live allowance.
//
// The TTL is 60 seconds because rule S1 says the allowance may be cached for at most that long.
// Anything longer would make the interface's claim about liveness indefensible.
//
// Kill, revoke and rotation do NOT wait for the TTL. A change stream evicts within milliseconds,
// because the kill switch's whole promise — the sentence an owner reads before confirming — is
// that the signer refuses "instantly", and a minute is not instantly.
type Snapshots struct {
	store   SnapshotStore
	ttl     time.Duration
	mu      sync.RWMutex
	byKey   map[string]entry
	byAgent map[string]string // agent id → key hash, so eviction can find the entry
}

type SnapshotStore interface {
	SnapshotForKey(ctx context.Context, keyHash string, now time.Time) (store.Snapshot, error)
}

type entry struct {
	snap store.Snapshot
	at   time.Time
}

func NewSnapshots(s SnapshotStore, ttl time.Duration) *Snapshots {
	return &Snapshots{
		store: s, ttl: ttl,
		byKey: map[string]entry{}, byAgent: map[string]string{},
	}
}

func (c *Snapshots) Get(ctx context.Context, keyHash string, now time.Time) (store.Snapshot, error) {
	c.mu.RLock()
	e, ok := c.byKey[keyHash]
	c.mu.RUnlock()
	if ok && now.Sub(e.at) < c.ttl {
		// The snapshot carries the moment it was read, so S1 can fail closed on a stale one rather
		// than trusting the cache's own bookkeeping.
		e.snap.TakenAt = e.at
		return e.snap, nil
	}

	snap, err := c.store.SnapshotForKey(ctx, keyHash, now)
	if err != nil {
		return store.Snapshot{}, err
	}
	snap.TakenAt = now

	c.mu.Lock()
	c.byKey[keyHash] = entry{snap: snap, at: now}
	c.byAgent[snap.Agent.ID] = keyHash
	c.mu.Unlock()
	return snap, nil
}

// Evict drops an agent's entry immediately. Called by the change-stream watcher on a kill, a
// revoke or a rotation, and by the race path before it re-reads.
func (c *Snapshots) Evict(agentID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if keyHash, ok := c.byAgent[agentID]; ok {
		delete(c.byKey, keyHash)
		delete(c.byAgent, agentID)
	}
}

// Keys caches unwrapped agent keys.
//
// The trade, stated out loud: an agent's private key is resident in this process's memory for up
// to ten minutes. The alternative is a KMS call on every signature, which makes AWS an
// availability dependency of every payment a customer's agent makes and puts the p99 in somebody
// else's hands.
//
// It does not change the damage ceiling — a leaked agent key can spend the remaining balance of
// one allowance and nothing else — but it is why this process has one endpoint, no template
// rendering, no file serving and no third-party HTTP client.
type Keys struct {
	unwrap UnwrapFunc
	ttl    time.Duration
	mu     sync.Mutex
	held   map[string]*keyEntry
}

type UnwrapFunc func(ctx context.Context, agentID string) (ed25519.PrivateKey, error)

type keyEntry struct {
	priv ed25519.PrivateKey
	at   time.Time
}

func NewKeys(unwrap UnwrapFunc, ttl time.Duration) *Keys {
	return &Keys{unwrap: unwrap, ttl: ttl, held: map[string]*keyEntry{}}
}

func (k *Keys) For(ctx context.Context, agentID string) (ed25519.PrivateKey, error) {
	k.mu.Lock()
	e, ok := k.held[agentID]
	k.mu.Unlock()
	if ok && time.Since(e.at) < k.ttl {
		return e.priv, nil
	}

	priv, err := k.unwrap(ctx, agentID)
	if err != nil {
		return nil, err
	}
	k.mu.Lock()
	if old, ok := k.held[agentID]; ok {
		zero(old.priv)
	}
	k.held[agentID] = &keyEntry{priv: priv, at: time.Now()}
	k.mu.Unlock()
	return priv, nil
}

// Evict wipes a key immediately. A rotation or a kill must not leave the old key usable.
func (k *Keys) Evict(agentID string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if e, ok := k.held[agentID]; ok {
		zero(e.priv)
		delete(k.held, agentID)
	}
}

// ZeroAll wipes everything, on shutdown.
func (k *Keys) ZeroAll() {
	k.mu.Lock()
	defer k.mu.Unlock()
	for id, e := range k.held {
		zero(e.priv)
		delete(k.held, id)
	}
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// Blockhashes holds one recent blockhash per network, refreshed by a goroutine.
//
// The hot path reads a value and never calls an RPC — which is what keeps the ban on outbound
// calls in the signing path true rather than aspirational.
type Blockhashes struct {
	mu   sync.RWMutex
	held map[network.Network]blockhashEntry
	ttl  time.Duration
}

type blockhashEntry struct {
	hash string
	at   time.Time
}

func NewBlockhashes(ttl time.Duration) *Blockhashes {
	return &Blockhashes{held: map[network.Network]blockhashEntry{}, ttl: ttl}
}

func (b *Blockhashes) Get(n network.Network) (string, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	e, ok := b.held[n]
	if !ok {
		return "", false
	}
	// A blockhash lasts 60–90 seconds on Solana. Serving one past its usefulness would produce a
	// transaction that cannot land, which the agent would treat as a payment that happened.
	if time.Since(e.at) > 90*time.Second {
		return "", false
	}
	return e.hash, true
}

func (b *Blockhashes) Set(n network.Network, hash string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.held[n] = blockhashEntry{hash: hash, at: time.Now()}
}

// Refresh keeps every configured network's blockhash current. It runs OUTSIDE the signing path.
func (b *Blockhashes) Refresh(ctx context.Context, nets []network.Network,
	fetch func(context.Context, network.Network) (string, error)) {
	tick := time.NewTicker(b.ttl)
	defer tick.Stop()
	for {
		for _, n := range nets {
			h, err := fetch(ctx, n)
			if err != nil {
				slog.Error("could not refresh the blockhash", "network", n, "err", err)
				continue
			}
			b.Set(n, h)
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			return
		}
	}
}
