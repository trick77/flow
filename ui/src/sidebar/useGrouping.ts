import { useCallback, useEffect, useRef, useState } from "react";

import { getProfile, updateProfile } from "../api";
import type { SidebarGrouping } from "../api/types.gen";
import { readStored, STORAGE_KEYS, writeStored } from "../shell/storage";
import { isGrouping } from "./group";

function stored(): SidebarGrouping {
  const v = readStored(STORAGE_KEYS.grouping);
  return isGrouping(v) ? v : "status";
}

// useGrouping is the sidebar's grouping. The owner profile holds it
// (sidebarGrouping); this browser keeps a copy, shown before the profile
// answers and used alone while the profile cannot be read or changed. A
// profile answer never overrides a choice made after it was asked for or
// while that choice is still being saved. profileRev changes on every
// profile.updated of the app stream: the profile is read again then.
export function useGrouping(
  profileRev = 0,
): [SidebarGrouping, (g: SidebarGrouping) => void] {
  const [grouping, setLocal] = useState<SidebarGrouping>(stored);
  // Bumped by every choice; a read started before the latest one is stale.
  const choices = useRef(0);
  const saving = useRef(0);

  useEffect(() => {
    let live = true;
    const asked = choices.current;
    getProfile().then(
      (p) => {
        if (!live || asked !== choices.current || saving.current > 0) return;
        if (!isGrouping(p.sidebarGrouping)) return;
        setLocal(p.sidebarGrouping);
        writeStored(STORAGE_KEYS.grouping, p.sidebarGrouping);
      },
      () => {
        // No profile: the browser's copy stays in charge.
      },
    );
    return () => {
      live = false;
    };
  }, [profileRev]);

  const choose = useCallback((g: SidebarGrouping) => {
    choices.current++;
    saving.current++;
    setLocal(g);
    writeStored(STORAGE_KEYS.grouping, g);
    // A refusal keeps the choice in this browser.
    updateProfile({ sidebarGrouping: g })
      .catch(() => {})
      .finally(() => {
        saving.current--;
      });
  }, []);

  return [grouping, choose];
}
