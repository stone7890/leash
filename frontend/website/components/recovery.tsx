"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { COPY } from "@/lib/domain/fault";

// The two recovery buttons from flow P3.
//
// The owner decides. Leash does not guess on their behalf, and it does not retry for the agent —
// the agent retries on its own cadence, and the next attempt goes through because the rule changed,
// not because we replayed anything.

export function Recovery({ agentId, host, agentName }: {
  agentId: string; host: string; agentName: string;
}) {
  const router = useRouter();
  const [done, setDone] = useState<"allowed" | "suppressed" | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function allow() {
    setBusy(true); setError(null);
    try {
      const res = await fetch(`/v1/agents/${agentId}/policy/allow-hosts`, {
        method: "PATCH",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ host }),
      });
      const body = await res.json();
      if (!res.ok) throw new Error(body?.error?.message ?? "could not add the host");
      setDone("allowed");
      router.refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not add the host");
    } finally { setBusy(false); }
  }

  async function suppress() {
    setBusy(true); setError(null);
    try {
      const res = await fetch(`/v1/agents/${agentId}/suppressions`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ host }),
      });
      if (!res.ok) {
        const body = await res.json().catch(() => null);
        throw new Error(body?.error?.message ?? "could not silence the alert");
      }
      setDone("suppressed");
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not silence the alert");
    } finally { setBusy(false); }
  }

  if (done === "allowed") {
    return (
      <p className="mt-3 rounded-[10px] border border-grn/30 bg-grnd p-3 text-xs text-grn">
        {host} added to {agentName}&apos;s allow list.{" "}
        {/* Never retroactive. The payment that was blocked stays blocked. */}
        <span className="text-mut">In effect from the next payment.</span>
      </p>
    );
  }

  if (done === "suppressed") {
    return (
      <p className="mt-3 rounded-[10px] border border-line bg-panel2 p-3 text-xs text-mut">
        Marked as expected. We won&apos;t alert on this endpoint again —{" "}
        {/* Said plainly, because a customer who believes they whitelisted the host will otherwise
            be confused by the next refusal. */}
        <span className="text-ink">the blocking continues</span>.
      </p>
    );
  }

  return (
    <div className="mt-3 space-y-2">
      <p className="text-[11px] text-mut">
        If this endpoint is legitimate, allow it and the agent&apos;s next attempt will go through.
      </p>
      <div className="flex flex-col gap-2">
        <button onClick={allow} disabled={busy}
                className="rounded-[10px] bg-grn px-3 py-1.5 text-xs font-medium text-bg
                           disabled:opacity-50">
          {busy ? "Saving…" : `Add ${host} to allow list`}
        </button>
        <button onClick={suppress} disabled={busy}
                className="rounded-[10px] border border-line px-3 py-1.5 text-xs text-mut
                           hover:text-ink disabled:opacity-50">
          This block is correct
        </button>
      </div>
      {error && <p className="text-xs text-red">{error}</p>}
      <p className="text-[11px] text-mut">{COPY.nothingMoved}.</p>
    </div>
  );
}
