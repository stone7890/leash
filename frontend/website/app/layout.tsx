import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "Leash — give every agent a budget",
  description: "Non-custodial spend management for AI agents on Solana.",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body className="min-h-screen bg-bg text-ink antialiased">{children}</body>
    </html>
  );
}
