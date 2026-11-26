import type { ReactNode } from "react";

// A small Markdown renderer for assistant text: paragraphs, headings, fenced
// code, pipe tables, bullet and numbered lists; inline code, bold, italics and
// http(s) links. It builds React nodes, never HTML strings, and copes with
// half-written text (an open fence, a table without its separator yet).

type Align = "left" | "right" | "center" | undefined;

type Block =
  | { type: "p"; text: string }
  | { type: "h"; text: string }
  | { type: "code"; lang: string; code: string }
  | { type: "table"; head: string[]; align: Align[]; rows: string[][] }
  | { type: "ul" | "ol"; items: string[] };

const FENCE = /^\s{0,3}(`{3,}|~{3,})\s*([\w+#.-]*)/;
const HEADING = /^\s{0,3}#{1,6}\s+(.*?)\s*#*\s*$/;
const BULLET = /^\s{0,3}[-*+]\s+(.*)$/;
const NUMBERED = /^\s{0,3}\d{1,9}[.)]\s+(.*)$/;
const TABLE_SEP = /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/;

function cells(line: string): string[] {
  let s = line.trim();
  if (s.startsWith("|")) s = s.slice(1);
  if (s.endsWith("|") && !s.endsWith("\\|")) s = s.slice(0, -1);
  return s.split(/(?<!\\)\|/).map((c) => c.trim().replace(/\\\|/g, "|"));
}

function alignOf(sep: string): Align {
  const s = sep.trim();
  const left = s.startsWith(":");
  const right = s.endsWith(":");
  if (left && right) return "center";
  if (right) return "right";
  if (left) return "left";
  return undefined;
}

function isTableStart(lines: string[], i: number): boolean {
  return (
    lines[i].includes("|") &&
    i + 1 < lines.length &&
    lines[i + 1].includes("-") &&
    TABLE_SEP.test(lines[i + 1])
  );
}

function startsBlock(lines: string[], i: number): boolean {
  const l = lines[i];
  return (
    FENCE.test(l) ||
    HEADING.test(l) ||
    BULLET.test(l) ||
    NUMBERED.test(l) ||
    isTableStart(lines, i)
  );
}

export function parseBlocks(src: string): Block[] {
  const lines = src.replace(/\r\n?/g, "\n").split("\n");
  const blocks: Block[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (line.trim() === "") {
      i++;
      continue;
    }
    const fence = FENCE.exec(line);
    if (fence) {
      const marker = fence[1];
      const body: string[] = [];
      i++;
      while (i < lines.length && !lines[i].trim().startsWith(marker)) {
        body.push(lines[i]);
        i++;
      }
      i++; // the closing fence, or past the end of half-written text
      blocks.push({ type: "code", lang: fence[2], code: body.join("\n") });
      continue;
    }
    const heading = HEADING.exec(line);
    if (heading) {
      blocks.push({ type: "h", text: heading[1] });
      i++;
      continue;
    }
    if (isTableStart(lines, i)) {
      const head = cells(line);
      const align = cells(lines[i + 1]).map(alignOf);
      const rows: string[][] = [];
      i += 2;
      while (
        i < lines.length &&
        lines[i].includes("|") &&
        lines[i].trim() !== ""
      ) {
        rows.push(cells(lines[i]));
        i++;
      }
      blocks.push({ type: "table", head, align, rows });
      continue;
    }
    const listRe = BULLET.test(line)
      ? BULLET
      : NUMBERED.test(line)
        ? NUMBERED
        : null;
    if (listRe) {
      const items: string[] = [];
      while (i < lines.length && lines[i].trim() !== "") {
        const m = listRe.exec(lines[i]);
        if (m) {
          items.push(m[1]);
        } else if (startsBlock(lines, i)) {
          break;
        } else {
          // A continuation line of the item above.
          items[items.length - 1] += " " + lines[i].trim();
        }
        i++;
      }
      blocks.push({ type: listRe === BULLET ? "ul" : "ol", items });
      continue;
    }
    const para: string[] = [line.trim()];
    i++;
    while (
      i < lines.length &&
      lines[i].trim() !== "" &&
      !startsBlock(lines, i)
    ) {
      para.push(lines[i].trim());
      i++;
    }
    blocks.push({ type: "p", text: para.join(" ") });
  }
  return blocks;
}

const INLINE =
  /(`+)([^`]|[^`][\s\S]*?[^`])\1(?!`)|\*\*(.+?)\*\*|__(.+?)__|\*(\S(?:.*?\S)?)\*|\b_(\S(?:.*?\S)?)_\b|\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g;

// renderInline turns one line of text into nodes; keys are prefixed with key.
export function renderInline(text: string, key = "i"): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  let n = 0;
  for (const m of text.matchAll(INLINE)) {
    const at = m.index ?? 0;
    if (at > last) out.push(text.slice(last, at));
    const k = `${key}.${n++}`;
    if (m[1] !== undefined) {
      out.push(
        <code key={k}>
          {m[2].trim() === "" ? m[2] : m[2].replace(/^ (.*) $/, "$1")}
        </code>,
      );
    } else if (m[3] !== undefined || m[4] !== undefined) {
      out.push(<strong key={k}>{renderInline(m[3] ?? m[4], k)}</strong>);
    } else if (m[5] !== undefined || m[6] !== undefined) {
      out.push(<em key={k}>{renderInline(m[5] ?? m[6], k)}</em>);
    } else {
      out.push(
        <a key={k} href={m[8]} target="_blank" rel="noopener noreferrer">
          {renderInline(m[7], k)}
        </a>,
      );
    }
    last = at + m[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

function renderBlock(b: Block, i: number): ReactNode {
  const k = `b${i}`;
  switch (b.type) {
    case "p":
      return <p key={k}>{renderInline(b.text, k)}</p>;
    case "h":
      return <h4 key={k}>{renderInline(b.text, k)}</h4>;
    case "code":
      return (
        <div key={k} className="code-wrap">
          {b.lang !== "" && <span className="code-lang">{b.lang}</span>}
          <pre>
            <code>{b.code}</code>
          </pre>
        </div>
      );
    case "table":
      return (
        <div key={k} className="tbl">
          <table>
            <thead>
              <tr>
                {b.head.map((c, j) => (
                  <th key={j} style={{ textAlign: b.align[j] }}>
                    {renderInline(c, `${k}.h${j}`)}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {b.rows.map((row, r) => (
                <tr key={r}>
                  {b.head.map((_, j) => (
                    <td key={j} style={{ textAlign: b.align[j] }}>
                      {renderInline(row[j] ?? "", `${k}.${r}.${j}`)}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      );
    case "ul":
    case "ol": {
      const List = b.type;
      return (
        <List key={k}>
          {b.items.map((item, j) => (
            <li key={j}>{renderInline(item, `${k}.${j}`)}</li>
          ))}
        </List>
      );
    }
  }
}

export function Markdown({ text }: { text: string }) {
  return <>{parseBlocks(text).map(renderBlock)}</>;
}
