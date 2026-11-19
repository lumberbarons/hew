// Package tracker provides hew's tracker in-process: initialize and open a
// local or GitHub tracker, read it, and write to it, with typed results and
// typed errors instead of flags, rendering and exit codes. The CLI is an
// adapter over this package, `pr` and `migrate`; DESIGN.md's "Tracker library"
// section is the contract and maps every command to the operation behind it.
//
// # Opening
//
// Open and Init take the worktree, and optionally the backend, repository and
// claimant, as arguments; nothing reads the process's working directory or
// environment, and nothing prints (notices go to Config.Notice). Detect reports
// which backend a worktree resolves to. A long-lived process should pin
// Config.Backend and treat a later Detect that disagrees as an error rather
// than following it.
//
// # Reading and writing
//
// Snapshot fetches the open issues once; Ready, List and Epics query it
// client-side, and Detail, Search, Triage and Primer carry the commands' own
// query policy, including the untriaged boundary: only Triage returns text no
// maintainer has vetted. Create, Edit, Start, Release, Close, Reopen, Block and
// Unblock write, and ApplyPlan creates a batch with a Checkpoint for resume.
// Every convention the CLI enforces on write is enforced here, and Block
// checks for dependency cycles before it mutates.
//
// # Identity and concurrency
//
// A local tracker has one claimant per worker: Config.Actor, or As on an open
// tracker. It is safe to write from several worktrees at once, because every
// mutation takes the tracker's lock and Start has exactly one winner. On GitHub
// every claim is the authenticated login, so two sessions are
// indistinguishable and As changes nothing; a consumer keeps its own record of
// which worker holds which issue. Read Identity for the effective claimant, and
// StartOutcomeOf to classify a refused Start. Every call takes a
// context.Context, and an interrupted multi-write operation reports the part
// that landed in a typed partial-write error.
//
// # Stability
//
// The API is experimental until hew 1.0 and may change in any v0.x minor
// release. A breaking change ships as a feat!: or fix!: commit, and CI diffs
// the exported surface of this package against the base commit. The GitHub
// backend needs the same gh credentials as the CLI; the local backend needs
// neither gh nor a network.
package tracker
