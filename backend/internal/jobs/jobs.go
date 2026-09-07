// Package jobs is the background-loop supervisor.
//
// In-process goroutines, not an external queue: one fewer moving part, and a job that cannot
// outlive its process cannot wake up after a deploy and act on a world that has moved.
//
// Every job is spawned ONCE PER NETWORK. A job that iterates networks inside its body is one `if`
// away from reading a sandbox record with a mainnet client, so the network is bound at spawn time,
// lives in the closure, and appears in every line the job logs.
package jobs

import (
	"context"
	"log/slog"
	"math/rand"
	"runtime/debug"
	"sync"
	"time"

	"github.com/stone7890/leash/internal/domain/network"
)

type Job struct {
	Name    string
	Network network.Network
	Every   time.Duration
	Run     func(ctx context.Context, now time.Time) error
}

type Supervisor struct{ wg sync.WaitGroup }

func Start(ctx context.Context, js []Job) *Supervisor {
	sup := &Supervisor{}
	for _, j := range js {
		sup.wg.Add(1)
		go func(j Job) {
			defer sup.wg.Done()
			log := slog.With("job", j.Name, "network", j.Network)

			// Jitter the first tick. Four jobs across two networks all firing at t=0 on every
			// instance of a rolling deploy is a self-inflicted thundering herd against the RPC.
			select {
			case <-time.After(time.Duration(rand.Int63n(int64(j.Every)))):
			case <-ctx.Done():
				return
			}

			t := time.NewTicker(j.Every)
			defer t.Stop()
			log.Info("job started", "every", j.Every)

			for {
				start := time.Now()
				func() {
					// One malformed document must not stop reconciliation for every other record.
					defer func() {
						if r := recover(); r != nil {
							log.Error("job panicked",
								"panic", r, "stack", string(debug.Stack()))
						}
					}()
					// A per-iteration deadline, so a hung RPC cannot wedge the loop for ever.
					c, cancel := context.WithTimeout(ctx, j.Every*3)
					defer cancel()
					if err := j.Run(c, start.UTC()); err != nil && ctx.Err() == nil {
						log.Error("job failed", "err", err, "took", time.Since(start))
					}
				}()

				select {
				case <-t.C:
					// A run that overruns its interval DROPS the next tick rather than stacking.
					// Two overlapping payment sweeps would race on the same payments.
				case <-ctx.Done():
					log.Info("job stopped")
					return
				}
			}
		}(j)
	}
	return sup
}

// Wait lets shutdown give an in-flight read-back the chance to finish and write its result, rather
// than losing it and re-doing the work next time.
func (s *Supervisor) Wait() { s.wg.Wait() }
