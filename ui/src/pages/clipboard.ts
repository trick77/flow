// copyText puts text on the clipboard and says whether it worked. The
// Clipboard API exists only in a secure context (HTTPS or localhost); a
// self-hosted server reached over plain HTTP has none, so the older
// execCommand copy through a hidden field stands in for it there.
export async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard && window.isSecureContext !== false) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // Refused (permissions, no focus): try the fallback.
    }
  }
  const field = document.createElement("textarea");
  field.value = text;
  field.setAttribute("readonly", "");
  field.style.position = "fixed";
  field.style.top = "0";
  field.style.left = "0";
  field.style.opacity = "0";
  document.body.appendChild(field);
  const active = document.activeElement as HTMLElement | null;
  field.select();
  let ok: boolean;
  try {
    ok =
      typeof document.execCommand === "function" &&
      document.execCommand("copy");
  } catch {
    ok = false;
  }
  field.remove();
  active?.focus();
  return ok;
}
