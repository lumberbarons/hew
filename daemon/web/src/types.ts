// Wire types for the daemon's REST API. They mirror the library's result types
// (tracker, pr, migrate), which now carry their own JSON shape, plus the few
// responses daemon/dto.go composes. The fixtures in src/fixtures are generated
// from the real Go responses by the daemon's tests, and the UI tests consume
// them, so a change to a Go response shape fails a test instead of the UI.

export interface Comment {
  author: string;
  createdAt: string;
  body: string;
}

export interface Issue {
  number: number;
  title: string;
  state: string;
  priority: string | null;
  type: string | null;
  areas: string[];
  assignees: string[];
  epic: boolean;
  inProgress: boolean;
  untriaged: boolean;
  parent: number | null;
  blockedBy: number[];
  blockedByTotal: number;
  openBlockers: number[];
  subIssues: number[];
  subIssuesTotal: number;
  subIssuesCompleted: number;
  createdAt: string;
  next?: number;
  body?: string;
  comments?: Comment[];
  commentsTotal?: number;
}

export interface Config {
  backend: string;
  repo: string;
  identity: string;
  dir: string;
  mainCheckout: string;
  id: string;
  actorEditable: boolean;
  pullRequests: boolean;
}

export interface Warning {
  kind: string;
  issue: number;
  cycle: number[];
  total: number;
  fetched: number;
}

export interface TextFinding {
  issue: number;
  source: string;
  comment: number;
  zeroWidth: boolean;
  bidiControl: boolean;
  unicodeTags: boolean;
  confusable: boolean;
}

export interface Finding {
  kind: string;
  tokens: string[];
}

export interface Board {
  ready: Issue[];
  claimed: Issue[];
  blocked: Issue[];
  epics: Issue[];
  untriaged: number;
  warnings: Warning[];
}

export interface Detail {
  issue: Issue;
  children: Issue[];
  next: number | null;
  textFindings: TextFinding[];
  warnings: Warning[];
}

export interface Primer {
  ready: Issue[];
  readyTotal: number;
  openTotal: number;
  inProgress: Issue[];
  inProgressTotal: number;
  epics: Issue[];
  epicsTotal: number;
  untriaged: number;
  warnings: Warning[];
  conventions: string;
}

export interface SearchResult {
  issues: Issue[];
  total: number;
  fetched: number;
}

export interface TriageResult extends SearchResult {
  textFindings: TextFinding[];
}

export interface CreateResult {
  issue: Issue;
  edges: { kind: string; to: number }[];
  findings: Finding[];
}

export interface CheckResult {
  ok: boolean;
  findings: Finding[];
}

export interface AppliedEntry {
  key: string;
  issue: number;
  created: boolean;
}

export interface ApplyResult {
  dryRun: boolean;
  created: number;
  wired: number;
  unwired: number;
  warnings: string[];
  mapping: Record<string, number>;
  applied: AppliedEntry[];
  wouldCreate: unknown[];
  alreadyCreated: Record<string, number>;
}

/** The body of every error response. */
export interface Problem {
  error: string;
  message: string;
  fields?: Record<string, unknown>;
}
