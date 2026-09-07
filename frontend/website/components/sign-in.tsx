"use client";

import { useState } from "react";
import { COPY } from "@/lib/domain/fault";

// The sign-in screen. The wallet IS the account: no password, no email address.
//
// The two notes below are not decoration. Each closes a specific misunderstanding a first-time
// visitor arrives with — that connecting might move something, and that they are about to touch
// real money.

type Provider = { name: string; get: () => SolanaProvider | undefined };

type SolanaProvider = {
  isPhantom?: boolean;
  connect: () => Promise<{ publicKey: { toString(): string } }>;
  signMessage: (m: Uint8Array, e?: string) => Promise<{ signature: Uint8Array }>;
};

const PROVIDERS: Provider[] = [
  { name: "Phantom",  get: () => (window as never as { phantom?: { solana?: SolanaProvider } }).phantom?.solana },
  { name: "Backpack", get: () => (window as never as { backpack?: SolanaProvider }).backpack },
  { name: "Solflare", get: () => (window as never as { solflare?: SolanaProvider }).solflare },
];

export function SignIn() {
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function connect(p: Provider) {
    setError(null);
    setBusy(p.name);
    try {
      const wallet = p.get();
      if (!wallet) {
        setError(`${p.name} was not detected in this browser.`);
        return;
      }
      const { publicKey } = await wallet.connect();
      const address = publicKey.toString();

      const nonceRes = await fetch("/v1/auth/siws/nonce", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ wallet: address }),
      });
      if (!nonceRes.ok) throw new Error("could not start sign-in");
      const { statement } = (await nonceRes.json()) as { statement: string };

      const signed = await wallet.signMessage(new TextEncoder().encode(statement), "utf8");
      const bs58 = (await import("bs58")).default;

      const verify = await fetch("/v1/auth/siws/verify", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          wallet: address,
          signature: bs58.encode(signed.signature),
          message: statement,
        }),
      });
      if (!verify.ok) throw new Error("sign in to continue");
      window.location.href = "/agents";
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not sign in");
    } finally {
      setBusy(null);
    }
  }

  return (
    <main className="mx-auto grid min-h-screen max-w-5xl items-center gap-10 px-6 py-16 md:grid-cols-2">
      <section>
        <div className="mono mb-8 text-sm text-grn">leash</div>
        <h1 className="text-4xl font-bold leading-tight">
          Give every agent a budget.<br />Not your wallet.
        </h1>
        <p className="mt-4 max-w-md text-mut">
          Delegate an on-chain spending cap to each AI agent. It pays for APIs over x402 by
          itself — inside the limits you set, with every cent attributed.
        </p>
        <p className="mt-8 text-sm text-mut">{COPY.nonCustodial}</p>
      </section>

      <section className="card">
        <h2 className="text-lg font-medium">Your wallet is your account</h2>
        <p className="mt-2 text-sm text-mut">
          Sign a message to prove it&apos;s you — no password, no email.
        </p>

        <div className="mt-6 space-y-2">
          {PROVIDERS.map((p) => (
            <button
              key={p.name}
              onClick={() => connect(p)}
              disabled={busy !== null}
              className="w-full rounded-[10px] border border-line bg-panel2 px-4 py-3 text-left
                         text-sm font-medium hover:border-pur disabled:opacity-50"
            >
              {busy === p.name ? `Waiting for ${p.name}…` : `Connect ${p.name}`}
            </button>
          ))}
        </div>

        {error && (
          <p className="mt-4 rounded-[10px] border border-red/30 bg-redd px-3 py-2 text-sm text-red">
            {error}
          </p>
        )}

        <p className="mt-6 text-xs text-mut">{COPY.connectingReadsOnly}</p>
        <p className="mt-2 text-xs text-mut">{COPY.sandboxFirst}</p>
        <p className="mt-4 text-xs text-mut">Leash never asks for your seed phrase.</p>
      </section>
    </main>
  );
}
