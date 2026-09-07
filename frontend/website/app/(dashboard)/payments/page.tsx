import Link from "next/link";
import { currentSession } from "@/lib/auth/session";
import { listActivity, timeline, verdictFor, getPayment } from "@/lib/store/queries";
import { formatShort } from "@/lib/domain/money";
import { PaymentPill, Pill } from "@/components/pills";
import { Timeline } from "@/components/timeline";
import { Recovery } from "@/components/recovery";
import { ActivityRow } from "@/components/activity-row";
import { RULE_COPY, COPY } from "@/lib/domain/fault";

export const dynamic = "force-dynamic";

export default async function PaymentsPage({
  searchParams,
}: { searchParams: Promise<{ open?: string }> }) {
  const session = (await currentSession())!;
  const { open } = await searchParams;

  // Payments AND blocks. A block produced no payment — nothing was signed — but an owner wants to
  // see it, because "the agent tried and was refused" is exactly as interesting as "the agent paid".
  const activity = await listActivity(session.org, "sandbox", 60);
  const selected = open ? activity.find((a) => idOf(a) === open) : undefined;

  const payment = selected?.kind === "payment" ? await getPayment(selected.payment.id) : null;
  const phases = payment ? await timeline(payment.id) : [];
  const verdict = payment ? await verdictFor(payment.id) : null;

  return (
    <div className="grid gap-6 lg:grid-cols-[1fr_400px]">
      <section>
        <h1 className="mb-3 text-sm font-medium text-mut">{activity.length} events</h1>
        <div className="overflow-hidden rounded-[10px] border border-line">
          <table className="w-full text-sm">
            <thead className="bg-panel text-left text-xs text-mut">
              <tr>
                <th className="px-4 py-2 font-medium">Time</th>
                <th className="px-4 py-2 font-medium">Agent</th>
                <th className="px-4 py-2 font-medium">Endpoint</th>
                <th className="px-4 py-2 font-medium">Amount</th>
                <th className="px-4 py-2 font-medium">Status</th>
              </tr>
            </thead>
            <tbody>
              {activity.length === 0 && (
                <tr><td colSpan={5} className="px-4 py-6 text-center text-xs text-mut">
                  Nothing yet. An agent&apos;s payments and refusals both appear here.
                </td></tr>
              )}
              {activity.map((a) => {
                const id = idOf(a);
                const isBlock = a.kind === "block";
                return (
                  <ActivityRow key={id} href={`/payments?open=${id}`} selected={id === open}>
                    <td className="mono px-4 py-2 text-xs text-mut">{a.at.slice(11, 19)}</td>
                    <td className="px-4 py-2 text-xs">
                      {isBlock ? a.agentName : a.payment.agentName}
                    </td>
                    <td className="mono px-4 py-2 text-xs">
                      <Link href={`/payments?open=${id}`} className="hover:text-grn">
                        {isBlock ? a.host : a.payment.host}
                      </Link>
                    </td>
                    <td className="mono px-4 py-2 text-xs text-grn">
                      ${formatShort(isBlock ? a.amount : a.payment.amount)}
                    </td>
                    <td className="px-4 py-2">
                      {isBlock
                        ? <Pill tone="bad">blocked · {RULE_COPY[a.code]?.name ?? a.failedRule}</Pill>
                        : <PaymentPill state={a.payment.state} />}
                    </td>
                  </ActivityRow>
                );
              })}
            </tbody>
          </table>
        </div>
      </section>

      <aside>
        {!selected && (
          <div className="card text-xs text-mut">
            Select an event to see its handshake.
          </div>
        )}

        {selected?.kind === "block" && (
          <div className="card sticky top-6">
            <h2 className="text-sm font-medium text-red">Payment blocked</h2>
            <p className="mt-1 text-xs text-mut">
              Result: blocked — {COPY.nothingMoved}.
            </p>

            <dl className="mono mt-3 space-y-1 text-xs">
              <Row k="Agent" v={selected.agentName} />
              <Row k="Endpoint" v={selected.host} />
              <Row k="Amount" v={`$${formatShort(selected.amount)} USDC`} />
            </dl>

            <h3 className="mb-2 mt-5 text-xs font-medium text-mut">402 handshake</h3>
            {/* The timeline stops at phase 2: nothing after it was reached, because nothing after
                it ran. */}
            <BlockedTimeline
              rule={selected.failedRule}
              code={selected.code}
              host={selected.host}
              reached={selected.checks.filter((c) => c.result !== "not_reached").length}
              total={selected.checks.length}
            />

            <Recovery agentId={selected.agentId} host={selected.host}
                      agentName={selected.agentName} />
          </div>
        )}

        {selected?.kind === "payment" && payment && (
          <div className="card sticky top-6">
            <h2 className="text-sm font-medium">Payment</h2>
            <dl className="mono mt-3 space-y-1 text-xs">
              <Row k="Agent" v={payment.agentName} />
              <Row k="Endpoint" v={payment.host} />
              <Row k="Amount" v={`$${formatShort(payment.amount)} USDC`} />
              <Row k="Pay to" v={`${payment.payTo.slice(0, 6)}…${payment.payTo.slice(-4)}`} />
              {payment.signature && (
                <Row k="Signature"
                     v={`${payment.signature.slice(0, 6)}…${payment.signature.slice(-4)}`} />
              )}
            </dl>
            <h3 className="mb-2 mt-5 text-xs font-medium text-mut">402 handshake</h3>
            <Timeline phases={phases} state={payment.state} verdict={verdict} />
          </div>
        )}
      </aside>
    </div>
  );
}

function BlockedTimeline({ rule, code, host, reached, total }: {
  rule: string; code: string; host: string; reached: number; total: number;
}) {
  return (
    <div className="space-y-2 text-xs">
      <Phase n={1} label="Challenge received" done />
      <div className="flex gap-3">
        <span className="mt-1 inline-block h-2 w-2 shrink-0 rounded-full bg-red" />
        <div>
          <div className="text-red">2. Blocked by rule: {RULE_COPY[code]?.name ?? rule}</div>
          <div className="mt-0.5 text-[11px] text-mut">
            {host} — {RULE_COPY[code]?.blocked ?? "refused"}
          </div>
          {/* Not reached, not failed. Saying "failed" would imply they ran. */}
          <div className="mt-0.5 text-[11px] text-mut">
            {Math.max(0, total - reached)} other checks not reached
          </div>
        </div>
      </div>
      {[3, 4, 5, 6].map((n) => (
        <Phase key={n} n={n} label={["", "", "", "Signed", "Replayed", "Broadcast", "Confirmed"][n]!} />
      ))}
    </div>
  );
}

function Phase({ n, label, done }: { n: number; label: string; done?: boolean }) {
  return (
    <div className="flex gap-3">
      <span className={`mt-1 inline-block h-2 w-2 shrink-0 rounded-full ${
        done ? "bg-grn" : "bg-line"}`} />
      <div className={done ? "text-ink" : "text-mut/50"}>{n}. {label}</div>
    </div>
  );
}

function idOf(a: { kind: string; payment?: { id: string }; id?: string }): string {
  return a.kind === "payment" ? a.payment!.id : a.id!;
}

function Row({ k, v }: { k: string; v: string }) {
  return (
    <div className="flex justify-between gap-4">
      <dt className="text-mut">{k}</dt>
      <dd className="text-ink">{v}</dd>
    </div>
  );
}
