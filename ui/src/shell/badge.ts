// projectBadge is the two-letter mark shown for a project wherever it
// appears: the first two letters or digits of its name, uppercase. Separators
// and other signs are skipped ("my-app" → "MY", "3d-viewer" → "3D"); a
// one-letter name gives one letter. A name with no letter or digit at all
// falls back to its first two characters, so the badge is never empty while
// the name is not.
export function projectBadge(name: string): string {
  const chars = [...name.trim()];
  const alnum = chars.filter((c) => /[\p{L}\p{N}]/u.test(c));
  return (alnum.length > 0 ? alnum : chars)
    .slice(0, 2)
    .join("")
    .toLocaleUpperCase("en-CH");
}
