"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";

/**
 * The two signer-tier limits, editable in place.
 *
 * They belong on this card and not in a settings page: the card already says which tier enforces
 * them, and an owner reading "per-payment max $0.05" after a refusal wants to change that number
 * where they are reading it.
 *
 * The cap and the expiry are NOT here. Those are the delegation, they live on chain, and moving
 * them takes a transaction the owner signs in their wallet — putting them in this form would offer
 * a signer-tier edit for something the card next door calls "enforced by Solana".
 */
export function PolicyLimits({ agentId, perTxMax, velocityMax, velocityWindowS }: {
  agentId: string; perTxMax: string; velocityMax: string; velocityWindowS: number;
}) {
  const router = useRouter();
  const [perTx, setPerTx] = useState(perTxMax);
  const [velocity, setVelocity] = useState(velocityMax);
  const [windowS, setWindowS] = useState(String(velocityWindowS));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  const dirty = perTx !== perTxMax || velocity !== velocityMax
    || windowS !== String(velocityWindowS);

  async function save() {
    setBusy(true); setError(null); setSaved(false);
    try {
      const res = await fetch(`/v1/agents/${agentId}/policy`, {
        method: "PATCH",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          per_tx_max: perTx.trim(),
          velocity_max: velocity.trim(),
          velocity_window_s: Number(windowS),
        }),
      });
      const body = await res.json();
      if (!res.ok) throw new Error(body?.error?.message ?? "could not save the limits");
      // Show what was STORED, not what was typed: "0.1" is accepted and stored as "0.100000", and
      // leaving the typed form on screen would keep the Save button lit over a change that has
      // already been made.
      setPerTx(String(body.per_tx_max));
      setVelocity(String(body.velocity_max));
      setWindowS(String(body.velocity_window_s));
      setSaved(true);
      router.refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not save the limits");
    } finally { setBusy(false); }
  }

  return (
    <div className="mt-4 space-y-2 border-t border-line pt-3">
      <div className="grid gap-2 sm:grid-cols-3">
        <Field label="Per-payment max">
          <div className="flex items-center gap-1">
            <span className="text-mut">$</span>
            <input value={perTx} onChange={(e) => setPerTx(e.target.value)}
                   inputMode="decimal" className={inputCls} />
          </div>
        </Field>
        <Field label="Window limit">
          <div className="flex items-center gap-1">
            <span className="text-mut">$</span>
            <input value={velocity} onChange={(e) => setVelocity(e.target.value)}
                   inputMode="decimal" className={inputCls} />
          </div>
        </Field>
        <Field label="Window">
          <div className="flex items-center gap-1">
            <input value={windowS} onChange={(e) => setWindowS(e.target.value)}
                   inputMode="numeric" className={inputCls} />
            <span className="text-mut">s</span>
          </div>
        </Field>
      </div>

      <div className="flex items-center gap-3">
        <button onClick={save} disabled={busy || !dirty}
                className="rounded-[10px] bg-grn px-3 py-1.5 text-xs font-medium text-bg
                           disabled:opacity-50">
          {busy ? "Saving…" : "Save limits"}
        </button>
        {/* Precise, because it is checkable: the signer holds a snapshot of the agent for up to 60
            seconds, and its change stream watches kills and revocations — not policies. So a saved
            limit binds on the next payment after that snapshot expires, and saying "immediately"
            would be a promise the cache does not keep. */}
        <span className="text-[11px] text-mut">
          {saved
            ? "Saved. Binding on the next payment after the signer's 60-second snapshot expires."
            : "Applies to the next payment, once the signer's 60-second snapshot expires."}
        </span>
      </div>
      {error && <p className="text-xs text-red">{error}</p>}
    </div>
  );
}

const inputCls = "mono w-full rounded-[10px] border border-line bg-panel2 px-2 py-1 text-xs " +
  "text-ink focus:border-grn focus:outline-none";

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block">
      <span className="mb-1 block text-[11px] text-mut">{label}</span>
      {children}
    </label>
  );
}
