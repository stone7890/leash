import Link from "next/link";
import { currentSession } from "@/lib/auth/session";
import { listAgents, liveAllowance, listPayments } from "@/lib/store/queries";
import { formatShort, remaining } from "@/lib/domain/money";
import { AllowancePill, PaymentPill } from "@/components/pills";
import { BudgetBar } from "@/components/budget-bar";

export const dynamic = "force-dynamic";

export default async function AgentsPage() {
  const session = (await currentSession())!;
  const agents = await listAgents(session.org, "sandbox");
  const rows = await Promise.all(agents.map(async (a) => ({
    agent: a, allowance: await liveAllowance(a.id),
  })));
  const feed = await listPayments(session.org, "sandbox", { limit: 12 });

  const spentToday = feed
    .filter((p) => p.state === "confirmed")
    .reduce((sum, p) => sum + p.amount, 0n);
  const blocked = 0;

  return (
    <div className="space-y-8">
      <div className="grid gap-4 sm:grid-cols-4">
        <Stat label="spent today" value={`$${formatShort(spentToday)}`} mono />
        <Stat label="active agents"
              value={String(rows.filter((r) => r.allowance?.state === "active").length)} />
        <Stat label="payments · 24h" value={String(feed.length)} />
        <Stat label="blocked by rules · 24h" value={String(blocked)} tone={blocked > 0 ? "red" : undefined} />
      </div>

      <section>
        <h2 className="mb-3 text-sm font-medium text-mut">Agents</h2>
        {rows.length === 0 ? (
          // Never a silent empty table.
          <div className="card text-center">
            <p className="text-sm text-mut">No agents yet.</p>
            <p className="mt-2 text-xs text-mut">
              Give one a budget and watch it pay for an API by itself.
            </p>
            <Link href="/onboarding"
                  className="mt-4 inline-block rounded-[10px] bg-grn px-4 py-2 text-sm
                             font-medium text-bg">
              Create your first agent
            </Link>
          </div>
        ) : (
          <div className="overflow-hidden rounded-[10px] border border-line">
            <table className="w-full text-sm">
              <thead className="bg-panel text-left text-xs text-mut">
                <tr>
                  <th className="px-4 py-2 font-medium">Agent</th>
                  <th className="px-4 py-2 font-medium">Budget</th>
                  <th className="px-4 py-2 font-medium">Expires</th>
                  <th className="px-4 py-2 font-medium">Status</th>
                </tr>
              </thead>
              <tbody>
                {rows.map(({ agent, allowance }) => {
                  // A pending row has NO action and is not clickable. An agent whose delegation
                  // has not been read back cannot be operated on, and offering a button that would
                  // fail is worse than offering none.
                  const pending = !allowance || allowance.state === "pending";
                  const name = (
                    <div>
                      <div className="font-medium">{agent.name}</div>
                      <div className="text-xs text-mut">
                        {agent.runsAs ?? "agent"}
                        {pending && " · awaiting confirmation"}
                      </div>
                    </div>
                  );
                  return (
                    <tr key={agent.id} className="border-t border-line">
                      <td className="px-4 py-3">
                        {pending ? name : <Link href={`/agents/${agent.id}`} className="hover:text-grn">{name}</Link>}
                      </td>
                      <td className="w-64 px-4 py-3">
                        {allowance ? (
                          <BudgetBar cap={allowance.cap}
                                     remaining={remaining(allowance.cap, allowance.drawn, allowance.reserved)} />
                        ) : <span className="text-xs text-mut">—</span>}
                      </td>
                      <td className="mono px-4 py-3 text-xs text-mut">
                        {allowance?.expiryTs
                          ? new Date(allowance.expiryTs).toISOString().slice(0, 16).replace("T", " ")
                          : "—"}
                      </td>
                      <td className="px-4 py-3">
                        {allowance ? <AllowancePill state={allowance.state} />
                                   : <span className="text-xs text-mut">no allowance</span>}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section>
        <h2 className="mb-3 text-sm font-medium text-mut">Live feed</h2>
        <div className="card space-y-2">
          {feed.length === 0 && <p className="text-sm text-mut">No payments yet.</p>}
          {feed.map((p) => (
            <Link key={p.id} href={`/payments?open=${p.id}`}
                  className="mono flex items-center gap-3 rounded-[10px] px-2 py-1.5 text-xs hover:bg-panel2">
              <span className="text-mut">{p.stateAt.slice(11, 19)}</span>
              <span className="text-ink">{p.agentName}</span>
              <span className="text-mut">→ {p.host}</span>
              <span className="text-grn">${formatShort(p.amount)}</span>
              <span className="ml-auto"><PaymentPill state={p.state} /></span>
            </Link>
          ))}
        </div>
      </section>
    </div>
  );
}

function Stat({ label, value, mono, tone }: {
  label: string; value: string; mono?: boolean; tone?: "red";
}) {
  return (
    <div className="card">
      <div className={`${mono ? "mono " : ""}text-2xl font-bold ${tone === "red" ? "text-red" : ""}`}>
        {value}
      </div>
      <div className="mt-1 text-xs text-mut">{label}</div>
    </div>
  );
}
