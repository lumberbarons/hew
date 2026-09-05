// Drives the installed opencode plugin the way opencode itself does — a
// session.created event, then the system-context transform — against a stub
// `$` so nothing shells out. Prints what the plugin produced as JSON on
// stdout; the Go test asserts against it.
//
// argv[2] is the plugin module to import.
import { pathToFileURL } from "node:url"

const calls = []

// opencode hands the plugin a tagged-template shell runner. The stub records
// what would have run and returns a sentinel in place of primer text, padded
// so the plugin's own trimming is exercised.
const $ = (strings, ...values) => {
  const command = String.raw({ raw: strings.raw }, ...values)
  return {
    cwd: (dir) => ({
      text: async () => {
        calls.push({ command, cwd: dir })
        return "  PRIMER-SENTINEL  \n"
      },
    }),
  }
}

const { HewPrimePlugin } = await import(pathToFileURL(process.argv[2]).href)
const plugin = await HewPrimePlugin({ $, worktree: "/stub/worktree" })

const session = (type, id) => plugin.event({ event: { type, properties: { info: { id } } } })

const transform = async (sessionID) => {
  const output = { system: [] }
  await plugin["experimental.chat.system.transform"]({ sessionID }, output)
  return output.system
}

await session("session.created", "s1")
const created = await transform("s1")
const cached = await transform("s1") // the same session must not prime twice
const lazy = await transform("no-event") // never announced: prime on demand
const anonymous = await transform(undefined) // no session: contribute nothing

await session("session.deleted", "s1")
const afterDelete = await transform("s1") // eviction means priming again

console.log(JSON.stringify({ created, cached, lazy, anonymous, afterDelete, calls }))
