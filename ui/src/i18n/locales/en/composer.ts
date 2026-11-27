// The composer: input, agent and model picker, permission modes, queue, and
// the start of a new session. Owned by the composer area.
export const composer = {
  placeholder: "Message",
  send: "Send message",
  queue: "Queue message",
  stop: "Stop the running turn",
  stopLabel: "Stop",
  queued: "Queued",
  sendNow: "Send now",
  remove: "Remove",
  modePlan: "Plan",
  modeAsk: "Ask",
  modeAcceptEdits: "Accept edits",
  modeAuto: "Auto",
  modeFullAccess: "Full access",
  modePlanHint: "Read and propose, no edits or commands",
  modeAskHint: "Approve every edit and command",
  modeAcceptEditsHint: "Edits apply, commands still ask",
  modeAutoHint: "Safe commands run, risky ones ask",
  modeFullAccessHint: "Nothing asks. Sandbox limits still apply",
  // The model and mode of a running session; changing them is not built.
  model: "Model",
  modelOf: "Model: {{agent}}, {{model}}",
  mode: "Permission mode",
  modeOf: "Permission mode: {{mode}}",
  fixedHint: "Set when the session started",
  // Starting a new session.
  newTitle: "New session",
  project: "Project",
  projectOf: "Project: {{name}}",
  startQuestion: "What should we work on in {{project}}?",
  startHint:
    "Each repository gets a worktree from its default branch, on one new branch, when you start.",
  repos: "Repositories for this session",
  reposNone: "Pick at least one repository.",
  startPlaceholder: "Describe the task",
  start: "Start session",
  starting: "Starting…",
  // Under the composer: where the session's agent runs.
  runsIn: "Runs in the {{project}} sandbox",
  agentUnavailable: "Not signed in",
  noAgent:
    "No agent can run yet: store a Claude token or an Anthropic API key in Settings.",
  openSettings: "Open Settings",
  noProject:
    "Create a project first: a session works in a project's repositories.",
  openProjects: "Open Projects",
  catalogLoading: "Loading agents…",
  catalogFailed: "The agents could not be loaded.",
  retry: "Try again",
  claudeAuth: "Claude credential",
  claudeAuthOf: "Claude credential: {{auth}}",
  // Before the model in the model picker (wide screens only).
  agentPrefix: "{{agent}} · ",
  authSubscriptionToken: "Subscription token",
  authSubscriptionLogin: "Subscription login",
  authApiKey: "API key",
};
