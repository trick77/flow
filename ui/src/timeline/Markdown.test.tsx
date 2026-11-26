import { render } from "@testing-library/react";

import { Markdown, parseBlocks } from "./Markdown";

function html(text: string): HTMLElement {
  return render(<Markdown text={text} />).container;
}

describe("Markdown", () => {
  it("renders the demo answer: inline code, a table, a closing paragraph", () => {
    const el = html(
      "Done. The width now lives in `user_prefs.sidebar_width` and comes back.\n\n| Step | Result | Time |\n| --- | --- | ---: |\n| go test | 1'284 passed | 38.2 s |\n\nNothing is pushed yet.",
    );
    expect(el.querySelectorAll("p")).toHaveLength(2);
    expect(el.querySelector("p code")?.textContent).toBe(
      "user_prefs.sidebar_width",
    );
    const ths = [...el.querySelectorAll("th")].map((th) => th.textContent);
    expect(ths).toEqual(["Step", "Result", "Time"]);
    const tds = el.querySelectorAll("td");
    expect([...tds].map((td) => td.textContent)).toEqual([
      "go test",
      "1'284 passed",
      "38.2 s",
    ]);
    expect((tds[2] as HTMLElement).style.textAlign).toBe("right");
  });

  it("renders fenced code with its language, unterminated too", () => {
    const el = html(
      'Before:\n\n```go\nschema := fmt.Sprintf("t_%x")\n```\nAfter',
    );
    expect(el.querySelector(".code-lang")?.textContent).toBe("go");
    expect(el.querySelector("pre code")?.textContent).toBe(
      'schema := fmt.Sprintf("t_%x")',
    );
    expect(el.querySelectorAll("p")).toHaveLength(2);
    const open = html("```\nhalf written");
    expect(open.querySelector("pre code")?.textContent).toBe("half written");
    expect(open.querySelector(".code-lang")).toBeNull();
  });

  it("renders lists, headings, bold, italics and links", () => {
    const el = html(
      "# Plan\n\n- A **queued** message\n  wraps here\n- _Send now_ and *Remove*\n\n1. one\n2. [two](https://example.com/x)\n## Next",
    );
    expect([...el.querySelectorAll("h4")].map((h) => h.textContent)).toEqual([
      "Plan",
      "Next",
    ]);
    const ul = el.querySelectorAll("ul li");
    expect(ul).toHaveLength(2);
    expect(ul[0].textContent).toBe("A queued message wraps here");
    expect(ul[0].querySelector("strong")?.textContent).toBe("queued");
    expect([...ul[1].querySelectorAll("em")].map((e) => e.textContent)).toEqual(
      ["Send now", "Remove"],
    );
    const a = el.querySelector("ol a") as HTMLAnchorElement;
    expect(a.href).toBe("https://example.com/x");
    expect(a.rel).toBe("noopener noreferrer");
    expect(el.querySelectorAll("ol li")).toHaveLength(2);
  });

  it("keeps markup characters that are not markup as text", () => {
    const el = html(
      "a * b * c, snake_case_name, [no](javascript:alert(1)) <b>x</b>",
    );
    expect(el.querySelector("em")).toBeNull();
    expect(el.querySelector("a")).toBeNull();
    expect(el.querySelector("b")).toBeNull();
    expect(el.textContent).toBe(
      "a * b * c, snake_case_name, [no](javascript:alert(1)) <b>x</b>",
    );
  });

  it("reads a table header without separator as a paragraph", () => {
    expect(parseBlocks("| a | b |")).toEqual([
      { type: "p", text: "| a | b |" },
    ]);
    expect(parseBlocks("| a | b |\n| :-: | :-- |\n| 1 |")).toEqual([
      {
        type: "table",
        head: ["a", "b"],
        align: ["center", "left"],
        rows: [["1"]],
      },
    ]);
  });

  it("ends a list at the next block", () => {
    expect(parseBlocks("- a\n```\nx\n```\r\ntext\n1) b")).toEqual([
      { type: "ul", items: ["a"] },
      { type: "code", lang: "", code: "x" },
      { type: "p", text: "text" },
      { type: "ol", items: ["b"] },
    ]);
  });
});
