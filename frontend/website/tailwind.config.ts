import type { Config } from "tailwindcss";

// The tokens are the prototype's, used unchanged. See docs/10-ui-spec.md.
//
// `grn` is reserved for money, on-chain enforcement and the primary call to action. Using it for a
// generic success state would erode the one signal that means "this is real money, and the chain
// agrees".
export default {
  content: ["./app/**/*.{ts,tsx}", "./components/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        bg: "#12101F",
        panel: "#1A1830",
        panel2: "#211E3B",
        line: "#2E2A52",
        ink: "#E9E7F4",
        mut: "#9B96BE",
        grn: "#14F195",
        grnd: "#0E3A2B",
        pur: "#9C6BFF",
        amb: "#FFC14D",
        red: "#FF6B81",
        redd: "#3A1620",
      },
      borderRadius: { DEFAULT: "10px", lg: "10px" },
      fontFamily: {
        sans: ["'Space Grotesk'", "ui-sans-serif", "system-ui", "sans-serif"],
        // Monospace for every amount, address, slot and key. Not decoration: it is what makes
        // 3xk…9Qe comparable against a block explorer at a glance.
        mono: ["'IBM Plex Mono'", "ui-monospace", "Menlo", "monospace"],
      },
    },
  },
  plugins: [],
} satisfies Config;
