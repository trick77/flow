import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { formatNumber } from "../i18n/format";

// useFormatDuration gives "41s", "2m 14s", "1h 05m". format.ts has no
// duration format; the units are timeline strings, the numbers formatNumber's.
export function useFormatDuration(): (ms: number) => string {
  const { t } = useTranslation("timeline");
  return useCallback(
    (ms: number) => {
      const total = Math.max(0, Math.floor(ms / 1000));
      const h = Math.floor(total / 3600);
      const m = Math.floor((total % 3600) / 60);
      const s = total % 60;
      const two = (n: number) => formatNumber(n, { minimumIntegerDigits: 2 });
      if (h > 0) {
        return t("durationHours", { h: formatNumber(h), m: two(m) });
      }
      if (m > 0) {
        return t("durationMinutes", { m: formatNumber(m), s: two(s) });
      }
      return t("durationSeconds", { s: formatNumber(s) });
    },
    [t],
  );
}

// useNow is the time in ms, ticking every second while active.
export function useNow(active: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, [active]);
  return now;
}
