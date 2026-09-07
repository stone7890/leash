"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { HandshakeLog } from "@/components/handshake-log";

/**
 * Run one real payment as this agent, from its own page.
 *
 * The same loop onboarding step 5 runs, and for the same reason: a real 402 from the sample
 * endpoint, a real evaluation of the eight rules, a real signature, a real settlement. Nothing
 * here is simulated except the decision to start it — which is why a refusal is as useful an
 * outcome as a payment, and is shown rather than treated as an error.
 *
 * It asks for the API KEY, and that is not an oversight. What the database holds is a SHA-256 of
 * it, so this page cannot produce the key however much it would like to: `POST /v1/sign` is the
 * only route an agent's credential opens, and giving the dashboard a way around it would make the
 * signer's one authenticated path a formality. The key stays in this component's state, goes to
 * the server once, and is never stored.
 */
export function SimulatePayment({ agentId, host }: { agentId: string; host: string }) {
  const router = useRouter();
  const [apiKey, setApiKey] = useState("");
  const [paymentId, setPaymentId] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function run() {
    setBusy(true); setError(null); setPaymentId(null);
    try {
      const res = await fetch("/v1/test-payments", {
        method: "POST",
        headers: {
          "content-type": "application/json",
          // One attempt, one identifier. Two clicks with the same one would replay the first
          // payment rather than making a second — I7, and the reason a loop must not reuse it.
          "idempotency-key": crypto.randomUUID(),
        },
        body: JSON.stringify({ agent_id: agentId, api_key: apiKey.trim() }),
      });
      const body = await res.json();
      if (!res.ok) throw new Error(body?.error?.message ?? "could not start the payment");
      setPaymentId(body.payment_id);
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not start the payment");
    } finally { setBusy(false); }
  }

  return (
    <section className="card space-y-3">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-medium">Simulate a payment</h2>
        <span className="mono text-xs text-mut">{host}</span>
      </div>
      <p className="text-xs text-mut">
        Runs the real 402 handshake against the sandbox&apos;s sample endpoint, as this agent.
        A refusal is a result, not a failure — the rule that stopped it is named.
      </p>

      <div className="flex flex-wrap gap-2">
        <input
          value={apiKey}
          onChange={(e) => setApiKey(e.target.value)}
          onKeyDown={(e) => { if (e.key === "Enter" && apiKey.trim() && !busy) void run(); }}
          placeholder="lk_test_… — this agent's API key"
          className="mono min-w-[280px] flex-1 rounded-[10px] border border-line bg-panel2 px-3
                     py-1.5 text-xs text-ink placeholder:text-mut"
        />
        <button onClick={run} disabled={busy || apiKey.trim().length === 0}
                className="rounded-[10px] bg-grn px-3 py-1.5 text-xs font-medium text-bg
                           disabled:opacity-50">
          {busy ? "Starting…" : "Pay $0.01"}
        </button>
      </div>
      <p className="text-[11px] text-mut">
        The key is shown once, when the agent is created. We store only its SHA-256, so this page
        cannot fill it in for you — and it is not saved when you paste it here.
      </p>

      {error && <p className="text-xs text-red">{error}</p>}

      {paymentId && (
        <div className="pt-1">
          {/* The same live log as onboarding: one line per handshake row, streamed as it is
              written. When it ends the figures above are stale, so the page is asked to re-read
              them — `reserved` moves at once, `drawn` after the indexer's next read-back. */}
          <HandshakeLog paymentId={paymentId} onDone={() => router.refresh()} />
        </div>
      )}
    </section>
  );
}
