// Numbers, dates and times in Flow's one locale: English text, Swiss
// conventions. 1'234'567.5, 26.11.2026, 14:05 (24-hour).
export const LOCALE = "en-CH";

// Explicit fields: the locale's defaults differ between ICU versions, these
// do not.
const dateFields: Intl.DateTimeFormatOptions = {
  day: "2-digit",
  month: "2-digit",
  year: "numeric",
};
const timeFields: Intl.DateTimeFormatOptions = {
  hour: "2-digit",
  minute: "2-digit",
  hourCycle: "h23",
};

const dateFormat = new Intl.DateTimeFormat(LOCALE, dateFields);
const timeFormat = new Intl.DateTimeFormat(LOCALE, timeFields);
const dateTimeFormat = new Intl.DateTimeFormat(LOCALE, {
  ...dateFields,
  ...timeFields,
});

// formatNumber groups thousands with an apostrophe; options as Intl takes
// them (maximumFractionDigits, style: "percent", ...).
export function formatNumber(
  value: number,
  options?: Intl.NumberFormatOptions,
): string {
  return new Intl.NumberFormat(LOCALE, options).format(value);
}

// formatDate is the day as dd.mm.yyyy, in the browser's time zone.
export function formatDate(date: Date): string {
  return dateFormat.format(date);
}

// formatTime is hh:mm on a 24-hour clock.
export function formatTime(date: Date): string {
  return timeFormat.format(date);
}

// formatDateTime is both, "26.11.2026, 14:05".
export function formatDateTime(date: Date): string {
  return dateTimeFormat.format(date);
}

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;
const WEEK = 7 * DAY;
const YEAR = 365 * DAY;

// Narrow units, no months: the narrow month ("3m") reads like minutes.
const agoSteps: readonly [number, number, string][] = [
  [HOUR, MINUTE, "minute"],
  [DAY, HOUR, "hour"],
  [WEEK, DAY, "day"],
  [YEAR, WEEK, "week"],
  [Infinity, YEAR, "year"],
];
const agoFormats = new Map(
  agoSteps.map(([, , unit]) => [
    unit,
    new Intl.NumberFormat(LOCALE, {
      style: "unit",
      unit,
      unitDisplay: "narrow",
    }),
  ]),
);

// formatAgo is how long ago something was, in the largest whole unit: "4m",
// "2h", "3d", "5w", "1y". Under a minute (and a time in the future) it is
// null: the caller says "now" in its own words.
export function formatAgo(ms: number): string | null {
  if (!(ms >= MINUTE)) return null;
  const [, size, unit] = agoSteps.find(([limit]) => ms < limit)!;
  return agoFormats.get(unit)!.format(Math.floor(ms / size));
}
