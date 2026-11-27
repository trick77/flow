import {
  LOCALE,
  formatAgo,
  formatDate,
  formatDateTime,
  formatNumber,
  formatTime,
} from "./format";

// Local time on both sides: the same wall clock in every time zone the tests
// run in.
const afternoon = new Date(2026, 10, 26, 14, 5, 9);
const morning = new Date(2026, 0, 3, 7, 4);

// ICU versions differ in the apostrophe they group with: ASCII ' or U+2019.
const apostrophe = "['’]";

describe("format", () => {
  it("uses en-CH", () => {
    expect(LOCALE).toBe("en-CH");
  });

  it("groups thousands with an apostrophe and keeps a decimal point", () => {
    expect(formatNumber(1234567)).toMatch(
      new RegExp(`^1${apostrophe}234${apostrophe}567$`),
    );
    expect(formatNumber(-9876.5)).toMatch(
      new RegExp(`^-9${apostrophe}876\\.5$`),
    );
    expect(formatNumber(999)).toBe("999");
  });

  it("passes Intl options through", () => {
    expect(formatNumber(0.256, { style: "percent" })).toBe("26%");
    expect(formatNumber(1.23456, { maximumFractionDigits: 2 })).toBe("1.23");
  });

  it("writes dates as dd.mm.yyyy", () => {
    expect(formatDate(afternoon)).toBe("26.11.2026");
    expect(formatDate(morning)).toBe("03.01.2026");
  });

  it("writes times on a 24-hour clock", () => {
    expect(formatTime(afternoon)).toBe("14:05");
    expect(formatTime(morning)).toBe("07:04");
    expect(formatTime(new Date(2026, 0, 1, 0, 30))).toBe("00:30");
  });

  it("writes date and time together", () => {
    expect(formatDateTime(afternoon)).toBe("26.11.2026, 14:05");
  });

  it("says how long ago in the largest whole narrow unit", () => {
    const min = 60_000;
    expect(formatAgo(59_999)).toBeNull();
    expect(formatAgo(-5 * min)).toBeNull();
    expect(formatAgo(Number.NaN)).toBeNull();
    expect(formatAgo(min)).toBe("1m");
    expect(formatAgo(59 * min)).toBe("59m");
    expect(formatAgo(2 * 60 * min + 5 * min)).toBe("2h");
    expect(formatAgo(3 * 24 * 60 * min)).toBe("3d");
    expect(formatAgo(15 * 24 * 60 * min)).toBe("2w");
    expect(formatAgo(800 * 24 * 60 * min)).toBe("2y");
  });
});
