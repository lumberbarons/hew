// Drives the installed opencode plugin the way opencode itself does — under
// both the V1 and V2 plugin APIs — against a fake `hew` on PATH, so nothing
// hits the network. Prints what each entrypoint produced as JSON on stdout;
// the Go test asserts against it.
//
// argv[2] is the plugin module to import.
import { chmodSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import path from "node:path"
import { pathToFileURL } from "node:url"

const SENTINEL = "PRIMER-SENTINEL"

// `execFile` runs the plugin's `hew` with cwd set to the plugin's directory,
// so that directory has to exist. The harness reports it back for the Go test
// to compare against the recorded call sites.
const worktree = realpathSync(mkdtempSync(path.join(tmpdir(), "hew-plugin-worktree-")))

// A fake `hew` executable: records the directory it ran in and prints a padded
// sentinel in place of primer text, so the plugin's own trimming is exercised.
const bin = mkdtempSync(path.join(tmpdir(), "hew-plugin-bin-"))
const callsFile = path.join(bin, "calls")
const hew = path.join(bin, "hew")
writeFileSync(
  hew,
  `#!/bin/sh\npwd >> ${JSON.stringify(callsFile)}\nprintf '  ${SENTINEL}  \\n'\n`,
)
chmodSync(hew, 0o755)
process.env.PATH = `${bin}${path.delimiter}${process.env.PATH ?? ""}`

const readCalls = () => {
  try {
    return readFileSync(callsFile, "utf8").split("\n").filter(Boolean)
  } catch {
    return []
  }
}
const resetCalls = () => writeFileSync(callsFile, "")

const { default: plugin } = await import(pathToFileURL(process.argv[2]).href)

// --- OpenCode 1 -------------------------------------------------------------
const v1 = await plugin.server({ worktree })
const v1Event = (type, id) => v1.event({ event: { type, properties: { info: { id } } } })
const v1Transform = async (sessionID) => {
  const output = { system: [] }
  await v1["experimental.chat.system.transform"]({ sessionID }, output)
  return output.system
}

await v1Event("session.created", "s1")
const v1Created = await v1Transform("s1")
const v1Cached = await v1Transform("s1") // the same session must not prime twice
const v1Lazy = await v1Transform("no-event") // never announced: prime on demand
const v1Anonymous = await v1Transform(undefined) // no session: contribute nothing
await v1Event("session.deleted", "s1")
const v1AfterDelete = await v1Transform("s1") // eviction means priming again
const v1Calls = readCalls()

// --- OpenCode 2 -------------------------------------------------------------
resetCalls()

// A fake public event stream: buffered pushes, consumed by the subscription
// loop `setup` starts in the background.
const makeEvents = () => {
  const buffer = []
  let pending = null
  let closed = false
  const subscribe = (options) => ({
    [Symbol.asyncIterator]() {
      return {
        next: async () => {
          if (closed || options?.signal?.aborted) return { value: undefined, done: true }
          if (buffer.length > 0) return { value: buffer.shift(), done: false }
          return await new Promise((resolve) => {
            pending = resolve
          })
        },
        return: async () => {
          closed = true
          return { value: undefined, done: true }
        },
      }
    },
  })
  const push = (event) => {
    if (pending) {
      const resolve = pending
      pending = null
      resolve({ value: event, done: false })
    } else {
      buffer.push(event)
    }
  }
  return { subscribe, push }
}

// Let the background subscription loop process an event it was just handed.
const settle = () => new Promise((resolve) => setTimeout(resolve, 10))

const events = makeEvents()
const hooks = {}
const ctx = {
  location: { directory: worktree },
  session: {
    hook: async (name, callback) => {
      hooks[name] = callback
      return {}
    },
  },
  event: { subscribe: events.subscribe },
}
const stop = await plugin.setup(ctx)

const v2Transform = async (sessionID) => {
  const system = []
  await hooks.context({ sessionID, system })
  return system.map((part) => part.text)
}

events.push({ type: "session.created", data: { sessionID: "s2", location: { directory: worktree } } })
await settle()
const v2Created = await v2Transform("s2")
const v2Cached = await v2Transform("s2")
const v2Lazy = await v2Transform("no-event")
const v2Anonymous = await v2Transform(undefined)
events.push({ type: "session.deleted", data: { sessionID: "s2" } })
await settle()
const v2AfterDelete = await v2Transform("s2")
await settle()
const v2Calls = readCalls()

if (typeof stop === "function") stop()

console.log(
  JSON.stringify({
    worktree,
    v1: {
      created: v1Created,
      cached: v1Cached,
      lazy: v1Lazy,
      anonymous: v1Anonymous,
      afterDelete: v1AfterDelete,
      calls: v1Calls,
    },
    v2: {
      created: v2Created,
      cached: v2Cached,
      lazy: v2Lazy,
      anonymous: v2Anonymous,
      afterDelete: v2AfterDelete,
      calls: v2Calls,
    },
  }),
)

rmSync(bin, { recursive: true, force: true })
rmSync(worktree, { recursive: true, force: true })
