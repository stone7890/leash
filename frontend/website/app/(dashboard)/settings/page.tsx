import { currentSession } from "@/lib/auth/session";
import { orgForWallet } from "@/lib/store/queries";

export const dynamic = "force-dynamic";

export default async function SettingsPage() {
  const session = (await currentSession())!;
  const org = await orgForWallet(session.wallet);

  return (
    <div className="max-w-2xl space-y-6">
      <section className="card">
        <h2 className="text-sm font-medium">Workspace</h2>
        <dl className="mono mt-3 space-y-1 text-xs">
          <Row k="Owner wallet" v={session.wallet} />
          <Row k="Network" v="Sandbox (local validator)" />
          <Row k="Currency" v="USDC" />
          <Row k="Plan" v={org?.plan ?? "free"} />
        </dl>
      </section>

      {/*
        Required by the specification, and required to be correct.

        One sentence of the original is FALSE under the shipping adapter: it says caps and expiries
        are both enforced by Solana. The SPL delegate has a delegated amount and no clock, so
        expiry is a signer rule — and invariant I3 does not permit an interface to claim a tier the
        chain does not deliver. The corrected wording is below, and the change is registered in
        docs/16-deck-conformance.md §B-4.
      */}
      <section className="card border-grn/30">
        <h2 className="text-sm font-medium">If Leash is ever down</h2>
        <p className="mt-3 text-sm text-mut">
          Your control doesn&apos;t depend on us. Every allowance can be revoked directly from your
          wallet or the Solana CLI — no Leash involved.
        </p>
        <p className="mt-3 text-sm text-mut">
          <span className="text-ink">Caps keep being enforced by Solana</span>, and revocation
          works from your wallet, no matter what happens to Leash. Signer-level rules — the allow
          list, the per-payment maximum, the ten-minute limit and the expiry — pause if the signer
          is unreachable, and an unreachable signer signs nothing.
        </p>
        <a href="/docs/if-leash-is-down"
           className="mt-4 inline-block text-sm text-grn hover:underline">
          Read the self-serve revoke guide →
        </a>
      </section>

      <section className="card">
        <h2 className="text-sm font-medium">What Leash holds</h2>
        <dl className="mt-3 space-y-2 text-xs">
          <Held k="Your private key" v="never" tone="grn" />
          <Held k="Your seed phrase" v="never — we do not ask, ever" tone="grn" />
          <Held k="Your funds" v="never" tone="grn" />
          <Held k="One key per agent" v="inside the signer, encrypted" tone="mut" />
        </dl>
        <p className="mt-3 text-xs text-mut">
          The worst a leaked agent key can do is spend the remaining balance of that one
          allowance — not the cap, not your wallet, and not your other agents.
        </p>
      </section>
    </div>
  );
}

function Row({ k, v }: { k: string; v: string }) {
  return (
    <div className="flex justify-between gap-4">
      <dt className="text-mut">{k}</dt>
      <dd className="break-all text-ink">{v}</dd>
    </div>
  );
}

function Held({ k, v, tone }: { k: string; v: string; tone: "grn" | "mut" }) {
  return (
    <div className="flex justify-between gap-4">
      <dt className="text-mut">{k}</dt>
      <dd className={tone === "grn" ? "text-grn" : "text-mut"}>{v}</dd>
    </div>
  );
}
