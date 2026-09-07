"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { base64ToBytes, bytesToBase64 } from "@/lib/domain/base64";

// The kill switch, flow P5.
//
// The modal states BOTH tiers before the owner confirms, in the order they happen, because the
// window between them is real and the owner should not discover it afterwards.

type SolanaProvider = {
  connect: () => Promise<{ publicKey: { toString(): string } }>;
  signTransaction: <T>(tx: T) => Promise<T>;
};

export function KillSwitch({ agentId, agentName, killed }: {
  agentId: string; agentName: string; killed: boolean;
}) {
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [stage, setStage] = useState<"idle" | "killing" | "revoking" | "done">("idle");
  const [error, setError] = useState<string | null>(null);

  async function stop() {
    setError(null);
    setStage("killing");
    try {
      const res = await fetch(`/v1/agents/${agentId}/revoke-intents`, {
        method: "POST",
        headers: { "content-type": "application/json", "idempotency-key": crypto.randomUUID() },
      });
      const body = await res.json();
      if (!res.ok) throw new Error(body?.error?.message ?? "could not stop the agent");

      // The flag is set the moment that call returns. Whatever happens below, the signer is
      // already refusing every new payment from this agent.
      router.refresh();

      if (!body.transaction) {
        setStage("done");
        return;
      }

      setStage("revoking");
      const provider =
        (window as never as { phantom?: { solana?: SolanaProvider } }).phantom?.solana
        ?? (window as never as { solflare?: SolanaProvider }).solflare;
      if (!provider) throw new Error("no wallet was detected — the signer is already refusing");
      await provider.connect();

      const { Transaction } = await import("@solana/web3.js");
      const tx = Transaction.from(base64ToBytes(body.transaction));
      const signed = await provider.signTransaction(tx);
      const encoded = bytesToBase64(
        (signed as unknown as { serialize(): Uint8Array }).serialize(),
      );

      const sent = await fetch(`/v1/agents/${agentId}/submit-revoke`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ transaction: encoded }),
      });
      if (!sent.ok) {
        const b = await sent.json().catch(() => null);
        throw new Error(b?.error?.message ?? "the chain refused the revoke");
      }
      // Still not "revoked". It stays `revoking…` until the indexer reads it back — saying
      // otherwise would tell an owner a compromised agent is contained when it is not yet.
      setStage("done");
      router.refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not complete the revoke");
      setStage("done");
    }
  }

  if (killed) {
    return (
      <span className="rounded-[10px] border border-line px-3 py-1.5 text-xs text-mut">
        {stage === "revoking" ? "Revoke pending" : "Stopped"}
      </span>
    );
  }

  return (
    <>
      <button onClick={() => setOpen(true)}
              className="rounded-[10px] border border-red/40 px-3 py-1.5 text-xs text-red
                         hover:bg-redd">
        Kill switch
      </button>

      {open && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4">
          <div className="w-full max-w-md rounded-[10px] border border-line bg-panel p-5">
            <h3 className="text-lg font-medium">Stop {agentName}?</h3>
            <p className="mt-3 text-sm text-mut">Two things happen, in order:</p>

            <ol className="mt-3 space-y-3 text-sm">
              <li>
                <span className="text-ink">Instantly:</span>{" "}
                <span className="text-mut">
                  The signer refuses every new payment from this agent.
                </span>
              </li>
              <li>
                <span className="text-ink">On-chain:</span>{" "}
                <span className="text-mut">
                  Your wallet signs a revoke — once confirmed, the allowance is gone even if Leash
                  is offline.
                </span>
              </li>
            </ol>

            {stage === "killing" && (
              <p className="mt-4 text-xs text-amb">
                Signer is refusing new payments. Waiting for the on-chain revoke…
              </p>
            )}
            {stage === "revoking" && (
              <p className="mt-4 text-xs text-amb">revoking… waiting for your wallet</p>
            )}
            {error && <p className="mt-4 text-xs text-red">{error}</p>}

            <div className="mt-5 flex gap-2">
              <button onClick={() => setOpen(false)}
                      className="rounded-[10px] border border-line px-4 py-2 text-sm text-mut">
                Cancel
              </button>
              <button onClick={stop} disabled={stage !== "idle"}
                      className="rounded-[10px] bg-red px-4 py-2 text-sm font-medium text-bg
                                 disabled:opacity-50">
                {stage === "idle" ? "Stop and revoke" : "Stopping…"}
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  );
}
