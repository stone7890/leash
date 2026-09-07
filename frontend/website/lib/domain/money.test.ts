import { describe, expect, it } from "vitest";
import { formatMoney, parseMoney, percentUsed, remaining } from "./money";

describe("money", () => {
  it("parses what it should", () => {
    expect(parseMoney("1")).toBe(1_000_000n);
    expect(parseMoney("0.31")).toBe(310_000n);
    expect(parseMoney("0.000001")).toBe(1n);
    expect(parseMoney("7.42")).toBe(7_420_000n);
  });

  it("refuses everything else", () => {
    for (const bad of ["", ".", "1.", ".5", "-1", "+1", "1e3", "0x10", "1,000",
                       "1.0000001", "$1", "NaN", "Infinity"]) {
      expect(() => parseMoney(bad), bad).toThrow();
    }
  });

  // The values a float-based parser gets wrong by one micro-USDC. Number("2.01") * 1e6 is
  // 2009999.9999999998, and truncating that loses a unit — which is the drift invariant I6 exists
  // to prevent.
  it("is exact where a float would not be", () => {
    for (const [s, want] of [["2.010000", 2_010_000n], ["2.030000", 2_030_000n],
                             ["4.020000", 4_020_000n], ["8.070000", 8_070_000n]] as const) {
      expect(parseMoney(s), s).toBe(want);
      expect(formatMoney(want)).toBe(s);
    }
  });

  // Above Number.MAX_SAFE_INTEGER: any implementation that passed through a double fails here.
  it("survives values a double cannot hold", () => {
    const v = parseMoney("999999999.999999");
    expect(v).toBe(999_999_999_999_999n);
    expect(formatMoney(v)).toBe("999999999.999999");
  });

  it("clamps remaining at zero rather than going negative", () => {
    expect(remaining(10n * 1_000_000n, 9n * 1_000_000n, 5n * 1_000_000n)).toBe(0n);
  });

  // The bar turns red at ≥95%.
  it("computes the percentage the bar uses", () => {
    const cap = 10n * 1_000_000n;
    expect(percentUsed(cap, 2_580_000n)).toBe(74);
    expect(percentUsed(cap, 0n)).toBe(100);
  });
});
