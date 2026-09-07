"use client";

import { useEffect, useState } from "react";
import { PHASE_LABEL, type Phase } from "@/lib/domain/state";

// The nine-line log from onboarding step 5.
//
// It is a REAL stream. Each line arrives as a server-sent event carrying a handshake row that the
// signer or the indexer actually wrote, in the order they wrote it. Nothing here is on a timer,
// and nothing is written in advance.
//
// That distinction is the whole point of the step: a scripted animation would be a demonstration
// that the product works, shown to a user for whom it might not.

type Event = {
  phase: Phase;
  writer: string;
  at: string;
  read_back_slot?: number | null;
  detail?: Record<string, unknown>;
};

export function HandshakeLog({ paymentId, onDone }: {
  paymentId: string;
  // Called once the stream closes, so a caller showing figures beside the log can re-read them.
  onDone?: () => void;
}) {
  const [events, setEvents] = useState<Event[]>([]);
  const [done, setDone] = useState(false);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    const source = new EventSource(`/v1/test-payments/${paymentId}/stream`);

    source.addEventListener("phase", (e) => {
      try {
        const parsed = JSON.parse((e as MessageEvent).data) as Event;
        setEvents((prev) =>
          prev.some((p) => p.phase === parsed.phase) ? prev : [...prev, parsed],
        );
      } catch { /* a malformed frame is not worth breaking the wizard over */ }
    });

    source.addEventListener("done", () => { setDone(true); source.close(); onDone?.(); });

    source.onerror = () => {
      // The stream is a convenience, not the record. If it drops, the timeline endpoint still has
      // everything — an onboarding session that spans a restart must not leave the user watching a
      // stalled wizard on the most important screen in the product.
      source.close();
      setFailed(true);
    };

    return () => source.close();
  }, [paymentId]);

  // Fall back to polling if the stream dropped before the payment resolved.
  useEffect(() => {
    if (!failed || done) return;
    const t = setInterval(async () => {
      try {
        const res = await fetch(`/v1/payments/${paymentId}/timeline`);
        if (!res.ok) return;
        const body = await res.json() as { phases: Event[] };
        setEvents(body.phases);
        if (body.phases.some((p) => p.phase === "confirmed")) { setDone(true); clearInterval(t); }
      } catch { /* keep trying */ }
    }, 2000);
    return () => clearInterval(t);
  }, [failed, done, paymentId]);

  return (
    <div className="mono rounded-[10px] border border-line bg-panel2 p-3 text-xs">
      {events.length === 0 && <div className="text-mut">starting the handshake…</div>}

      {events.map((e) => (
        <div key={e.phase} className="flex gap-2 py-0.5">
          <span className="text-grn">✓</span>
          <span className="text-ink">{PHASE_LABEL[e.phase]}</span>
          <span className="text-mut">{describe(e)}</span>
        </div>
      ))}

      {!done && events.length > 0 && (
        <div className="flex gap-2 py-0.5 text-mut">
          <span>…</span>
          <span>waiting for on-chain read-back</span>
        </div>
      )}

      {done && (
        <div className="mt-2 border-t border-line pt-2 text-grn">
          Your agent is wired up.
        </div>
      )}
    </div>
  );
}

function describe(e: Event): string {
  const d = e.detail ?? {};
  if (typeof d.amount === "string") return `· $${d.amount}`;
  if (typeof d.passed === "number") return `· ${d.passed}/${d.total} checks`;
  if (typeof d.allowance === "string") return `· ${String(d.allowance).slice(0, 6)}…`;
  if (e.read_back_slot) return `· slot ${e.read_back_slot}`;
  return "";
}
