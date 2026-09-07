package signer

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/stone7890/leash/internal/store"
)

// Timeline is the bounded queue that carries handshake phases 1–3 off the critical path.
//
// It is BOUNDED deliberately. An unbounded queue turns a database stall into an out-of-memory
// kill, which takes down the signer and stops every payment — a far worse outcome than the thing
// it was trying to avoid. On a full queue an event is dropped and counted: a lost timeline row is
// an audit gap to alarm on, not a reason to fail a payment that has already been signed and
// returned to the agent.
type Timeline struct {
	ch      chan store.HandshakeEvent
	store   TimelineStore
	dropped uint64
	mu      sync.Mutex
	wg      sync.WaitGroup
}

type TimelineStore interface {
	AppendPhase(ctx context.Context, e store.HandshakeEvent) error
}

func NewTimeline(s TimelineStore, capacity int) *Timeline {
	return &Timeline{ch: make(chan store.HandshakeEvent, capacity), store: s}
}

func (t *Timeline) Enqueue(e store.HandshakeEvent) {
	select {
	case t.ch <- e:
	default:
		t.mu.Lock()
		t.dropped++
		n := t.dropped
		t.mu.Unlock()
		slog.Error("the timeline queue is full — an audit row was dropped",
			"payment_id", e.PaymentID, "phase", e.Phase, "dropped_total", n)
	}
}

// Run drains the queue. Several writers, because a slow write must not hold up the ones behind it.
func (t *Timeline) Run(ctx context.Context, writers int) {
	for i := 0; i < writers; i++ {
		t.wg.Add(1)
		go func() {
			defer t.wg.Done()
			for {
				select {
				case e := <-t.ch:
					t.write(ctx, e)
				case <-ctx.Done():
					// Drain what is already queued before leaving. These are audit rows for
					// payments that have already happened.
					for {
						select {
						case e := <-t.ch:
							t.write(context.WithoutCancel(ctx), e)
						default:
							return
						}
					}
				}
			}
		}()
	}
}

func (t *Timeline) write(ctx context.Context, e store.HandshakeEvent) {
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	// A duplicate is not an error: `one_phase_per_payment` refuses it, and the store reports that
	// as success. It is what makes every writer safely re-runnable.
	if err := t.store.AppendPhase(c, e); err != nil {
		slog.Error("could not write a handshake phase",
			"payment_id", e.PaymentID, "phase", e.Phase, "err", err)
	}
}

// Wait lets shutdown drain the queue within the grace period.
func (t *Timeline) Wait() { t.wg.Wait() }

// Dropped is what the health endpoint reports, so a full queue is visible rather than silent.
func (t *Timeline) Dropped() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dropped
}
