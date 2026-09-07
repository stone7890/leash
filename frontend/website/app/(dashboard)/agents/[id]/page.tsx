import { notFound } from "next/navigation";
import { currentSession } from "@/lib/auth/session";
import { getAgent, getPolicy, liveAllowance, listPayments } from "@/lib/store/queries";
import { formatShort, remaining } from "@/lib/domain/money";
import { AllowancePill, PaymentPill, TierBadge } from "@/components/pills";
import { BudgetBar } from "@/components/budget-bar";
import { KillSwitch } from "@/components/kill-switch";

export const dynamic = "force-dynamic";

export default async function AgentPage({ params }: { params: Promise<{ id: string }> }) {
  const session = (await currentSession())!;
  const { id } = await params;

  const agent = await getAgent(session.org, id);
  if (!agent) notFound();

  const [policy, allowance, payments] = await Promise.all([
    getPolicy(agent.id),
    liveAllowance(agent.id),
    listPayments(session.org, agent.network, { agentId: agent.id, limit: 50 }),
  ]);

  return (
    <div className="space-y-8">
      <header className="flex items-center gap-3">
        <h1 className="text-xl font-bold">{agent.name}</h1>
        {allowance && <AllowancePill state={allowance.state} />}
        {agent.killed && allowance?.state !== "revoked" && (
          // Not "revoked". It says `revoking…` until the chain agrees — telling an owner that a
          // compromised agent is contained before the read-back is the trap flow P5 names.
          <span className="text-xs text-amb">revoking…</span>
        )}
        <div className="ml-auto">
          <KillSwitch agentId={agent.id} agentName={agent.name} killed={agent.killed} />
        </div>
      </header>

      {allowance && (
        <section className="grid gap-4 md:grid-cols-2">
          <div className="tier-onchain">
            <div className="mb-3 flex items-center justify-between">
              <h2 className="text-sm font-medium">Enforced by Solana</h2>
              <TierBadge tier="onchain" />
            </div>
            <p className="mb-4 text-xs text-mut">
              Cannot be bypassed — not by the agent, not by Leash.
            </p>
            <BudgetBar cap={allowance.cap}
                       remaining={remaining(allowance.cap, allowance.drawn, allowance.reserved)} />
            <dl className="mono mt-4 space-y-1 text-xs">
              <Row k="Spending cap" v={`$${formatShort(allowance.cap)} USDC`} />
              <Row k="Address" v={`${allowance.onchainAddr.slice(0, 6)}…${allowance.onchainAddr.slice(-4)}`} />
              {/* I2: the number and the slot it was read at travel together. */}
              <Row k="Read at slot"
                   v={allowance.lastReadSlot
                       ? allowance.lastReadSlot.toLocaleString("en-GB").replace(/,/g, " ")
                       : "not yet read"} />
            </dl>
          </div>

          <div className="tier-signer">
            <div className="mb-3 flex items-center justify-between">
              <h2 className="text-sm font-medium">Enforced by Leash signer</h2>
              <TierBadge tier="signer" />
            </div>
            <p className="mb-4 text-xs text-mut">
              Checked before every signature. Applies to the next payment after you save.
            </p>
            <dl className="mono space-y-1 text-xs">
              <Row k="Per-payment max" v={policy ? `$${formatShort(policy.perTxMax)}` : "—"} />
              <Row k="10-minute limit" v={policy ? `$${formatShort(policy.velocityMax)}` : "—"} />
              {/* The expiry tier comes from the allowance, not a constant. Under the SPL delegate
                  it is signer-enforced, and saying otherwise would break invariant I3. */}
              <Row k={`Expires (${allowance.expiryTier})`}
                   v={allowance.expiryTs
                       ? new Date(allowance.expiryTs).toISOString().slice(0, 16).replace("T", " ")
                       : "—"} />
            </dl>
            <div className="mt-3 flex flex-wrap gap-1.5">
              {policy?.allowAll ? (
                <span className="rounded-full border border-amb/40 bg-panel2 px-2 py-0.5 text-xs text-amb">
                  all endpoints allowed
                </span>
              ) : (
                policy?.allowHosts.map((h) => (
                  <span key={h} className="mono rounded-full border border-line bg-panel2 px-2 py-0.5 text-xs">
                    {h}
                  </span>
                ))
              )}
            </div>
          </div>
        </section>
      )}

      <section>
        <h2 className="mb-3 text-sm font-medium text-mut">Payments</h2>
        <div className="overflow-hidden rounded-[10px] border border-line">
          <table className="w-full text-sm">
            <thead className="bg-panel text-left text-xs text-mut">
              <tr>
                <th className="px-4 py-2 font-medium">Time</th>
                <th className="px-4 py-2 font-medium">Endpoint</th>
                <th className="px-4 py-2 font-medium">Amount</th>
                <th className="px-4 py-2 font-medium">Status</th>
                <th className="px-4 py-2 font-medium">Signature</th>
              </tr>
            </thead>
            <tbody>
              {payments.length === 0 && (
                <tr><td colSpan={5} className="px-4 py-6 text-center text-xs text-mut">
                  No payments yet.
                </td></tr>
              )}
              {payments.map((p) => (
                <tr key={p.id} className="border-t border-line">
                  <td className="mono px-4 py-2 text-xs text-mut">{p.stateAt.slice(11, 19)}</td>
                  <td className="mono px-4 py-2 text-xs">{p.host}</td>
                  <td className="mono px-4 py-2 text-xs text-grn">${formatShort(p.amount)}</td>
                  <td className="px-4 py-2"><PaymentPill state={p.state} /></td>
                  <td className="mono px-4 py-2 text-xs text-mut">
                    {p.signature ? `${p.signature.slice(0, 6)}…${p.signature.slice(-4)}` : "not signed"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}

function Row({ k, v }: { k: string; v: string }) {
  return (
    <div className="flex justify-between gap-4">
      <dt className="text-mut">{k}</dt>
      <dd className="text-ink">{v}</dd>
    </div>
  );
}
