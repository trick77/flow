// The drawer's line icons (the mock's i-terminal and i-x), stroked like the
// shell's (.i in index.css). Decorative: the control around one is labelled.
export function TerminalIcon() {
  return (
    <svg className="i" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <rect x="3" y="4.5" width="18" height="15" rx="2.5" />
      <path d="M7.5 9.5l3 2.5-3 2.5M13 15h4" />
    </svg>
  );
}

export function XIcon() {
  return (
    <svg className="i" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <path d="M6.5 6.5l11 11M17.5 6.5l-11 11" />
    </svg>
  );
}
