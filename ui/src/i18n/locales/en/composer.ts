// The composer: input, agent and model picker, permission modes, queue, and
// the start of a new session. Owned by the composer area.
export const composer = {
  placeholder: "Message",
  send: "Send message",
  queue: "Queue message",
  stop: "Stop the running turn",
  stopLabel: "Stop",
  queued: "Queued",
  queueLabel: "Queued messages",
  sendNow: "Send now",
  remove: "Remove",
  // While the agent waits on a question, the composer answers it.
  answerPlaceholder: "Answer the question",
  answer: "Send answer",
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
  // The model and mode pickers.
  model: "Model",
  modelOf: "Model: {{agent}}, {{model}}",
  mode: "Permission mode",
  modeOf: "Permission mode: {{mode}}",
  modeCapped: "Capped by the parent session",
  modelNextTurn: "Applies to the next turn",
  // The effort picker: how much the agent thinks before it answers.
  effort: "Effort",
  effortOf: "Effort: {{effort}}",
  effortDefault: "Agent default",
  effortDefaultHint: "The agent's own level for the model",
  effortLow: "Low",
  effortMedium: "Medium",
  effortHigh: "High",
  effortXhigh: "Extra high",
  effortMax: "Max",
  effortLowered: "A model without a level runs it lower",
  settingsFailed: "The change was not applied.",
  // Starting a new session.
  newTitle: "New session",
  project: "Project",
  projectOf: "Project: {{name}}",
  startQuestion: "What should we work on in {{project}}?",
  startHint:
    "Each repository gets a worktree from its default branch, on one new branch, when you start.",
  repos: "Repositories in this session",
  reposNone: "Pick at least one repository.",
  // A project with one repository: nothing to choose.
  workingIn: "Working in {{repo}}",
  // More repositories than chips fit: the menu button and its menu.
  repoPicked_one: "{{picked}} of {{n}} repository",
  repoPicked_other: "{{picked}} of {{n}} repositories",
  // The menu button's name; picked is repoPicked's text.
  reposOf: "Repositories in this session: {{picked}}",
  repoSearch: "Search repositories",
  repoSearchPlaceholder: "Search repositories",
  repoSearchNone: "No repository matches.",
  repoAll: "Select all",
  repoNone: "Select none",
  // A long project menu.
  projectSearch: "Search projects",
  projectSearchPlaceholder: "Search projects",
  projectSearchNone: "No project matches.",
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
  projectsLoading: "Loading projects…",
  projectsFailed: "The projects could not be loaded.",
  // After a start whose answer did not arrive (the session may exist).
  startLostChecking: "Checking whether the session started…",
  startLost:
    "If the session started, it is in the list now. Sending again does not start it twice.",
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
