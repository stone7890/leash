import Link from "next/link";
import { redirect } from "next/navigation";
import { currentSession } from "@/lib/auth/session";
import { latestSlot } from "@/lib/store/queries";

export const dynamic = "force-dynamic";

const NAV = [
  { href: "/agents", label: "Agents" },
  { href: "/payments", label: "Payments" },
  { href: "/alerts", label: "Alerts" },
  { href: "/settings", label: "Settings" },
];

export default async function DashboardLayout({ children }: { children: React.ReactNode }) {
  const session = await currentSession();
  if (!session) redirect("/");

  // The slot chip is invariant I2 made into furniture: the interface can always answer
  // "as of when?". It is read from what has actually been read back, never invented.
  const slot = await latestSlot("sandbox");

  return (
    <div className="min-h-screen">
      <header className="border-b border-line">
        <div className="mx-auto flex max-w-6xl items-center gap-4 px-6 py-3">
          <Link href="/agents" className="mono text-sm text-grn">leash</Link>

          <nav className="flex gap-1">
            {NAV.map((n) => (
              <Link key={n.href} href={n.href}
                className="rounded-[10px] px-3 py-1.5 text-sm text-mut hover:bg-panel hover:text-ink">
                {n.label}
              </Link>
            ))}
          </nav>

          <div className="ml-auto flex items-center gap-3">
            <Link href="/onboarding"
                  className="rounded-[10px] bg-grn px-3 py-1.5 text-xs font-medium text-bg">
              New agent
            </Link>
            {/* Always visible. A screen on which you cannot tell which network you are on is
                forbidden — that is invariant I8 in the interface. */}
            <span className="rounded-full border border-pur/40 bg-panel px-2.5 py-0.5 text-xs text-pur">
              Sandbox
            </span>
            <span className="mono rounded-full border border-line bg-panel px-2.5 py-0.5 text-xs text-mut"
                  title="latest indexed slot">
              {slot ? `slot ${slot.toLocaleString("en-GB").replace(/,/g, " ")}` : "no slot yet"}
            </span>
            <span className="mono text-xs text-mut">
              {session.wallet.slice(0, 4)}…{session.wallet.slice(-4)}
            </span>
          </div>
        </div>
      </header>

      {/* The sandbox banner. Never hidden, for the same reason as the chip. */}
      <div className="border-b border-line bg-panel">
        <div className="mx-auto max-w-6xl px-6 py-2 text-xs text-mut">
          You&apos;re on the <span className="text-pur">sandbox</span> — a test network with
          play-money USDC.
        </div>
      </div>

      <main className="mx-auto max-w-6xl px-6 py-8">{children}</main>
    </div>
  );
}
