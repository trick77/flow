// routeFetch answers fetch calls by "METHOD path" (query left out); an
// answer may be a function of the call, or a list taken one per call (the
// last one stays). Every call is recorded.
export interface FetchCall {
  method: string;
  url: string;
  body?: unknown;
}

export type Answer = Response | ((c: FetchCall) => Response);

export const json = (status: number, body?: unknown) =>
  new Response(body === undefined ? null : JSON.stringify(body), { status });

export function routeFetch(routes: Record<string, Answer | Answer[]>) {
  const calls: FetchCall[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const call: FetchCall = {
        method: init?.method ?? "GET",
        url: String(input),
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
      };
      calls.push(call);
      const key = `${call.method} ${call.url.split("?")[0]}`;
      const r = routes[key];
      if (!r) throw new Error("no route for " + key);
      let a: Answer;
      if (Array.isArray(r)) a = r.length > 1 ? r.shift()! : r[0];
      else a = r;
      // A Response body is read once: hand out a fresh copy.
      return typeof a === "function" ? a(call) : a.clone();
    }),
  );
  return calls;
}

export function connections(github: object) {
  return json(200, { github, claude: { signedIn: false }, apiKeys: [] });
}
