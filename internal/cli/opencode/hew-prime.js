// @hew-managed — installed by `hew hooks install opencode`; remove with
// `hew hooks remove opencode`.
//
// Runs `hew prime` once per session and injects the cached output as system
// context, so the primer is present before the first turn and never renders
// as a chat message.
//
// Dual OpenCode plugin entrypoint, default-exported as `{ id, setup, server }`:
// - OpenCode 2 calls `setup(ctx)`; the definition is written literally instead
//   of via `Plugin.define` so the file needs no `@opencode/plugin` resolution.
// - OpenCode 1 (>= 1.18.29) calls `server(input)` and uses the returned hooks.
//
// The two APIs are separate: the V1 event payload is `event.properties.info.id`
// and its system transform pushes strings, while V2 reads `event.data.sessionID`
// and pushes text system parts. They share the `hew prime` runner and cache.
import { execFile } from "node:child_process"

// Run `hew prime` in `directory`; a missing binary or a failing command yields
// no primer rather than an error. `execFile` (not the Bun shell) keeps the
// module free of `@opencode/plugin` resolution under either host.
const prime = (directory) =>
  new Promise((resolve) => {
    execFile("hew", ["prime"], { cwd: directory }, (error, stdout) => {
      resolve(error ? "" : stdout)
    })
  })

// A once-per-session cache of in-flight primer promises, scoped to the
// directory this plugin instance was loaded for.
const primerCache = (directory) => {
  const primers = new Map()
  return {
    get(sessionID) {
      let primer = primers.get(sessionID)
      if (primer === undefined) {
        primer = prime(directory)
        primers.set(sessionID, primer)
      }
      return primer
    },
    forget(sessionID) {
      primers.delete(sessionID)
    },
  }
}

export default {
  id: "hew.prime",

  // OpenCode 2.
  async setup(ctx) {
    const directory = ctx.location?.directory ?? process.cwd()
    const cache = primerCache(directory)
    const controller = new AbortController()

    // Warm the primer as soon as a session in this location is created, and
    // drop it when the session goes away. Events are global, so only warm
    // sessions that belong to this plugin's location.
    void (async () => {
      try {
        for await (const event of ctx.event.subscribe({ signal: controller.signal })) {
          if (event.type === "session.created") {
            if (event.data?.location?.directory !== directory) continue
            if (event.data?.sessionID) cache.get(event.data.sessionID)
          } else if (event.type === "session.deleted") {
            if (event.data?.sessionID) cache.forget(event.data.sessionID)
          }
        }
      } catch {}
    })()

    await ctx.session.hook("context", async (event) => {
      if (!event.sessionID) return
      const text = await cache.get(event.sessionID)
      if (text && text.trim()) event.system.push({ type: "text", text: text.trim() })
    })

    return () => controller.abort()
  },

  // OpenCode 1.
  async server(input) {
    const directory = input.worktree || input.directory
    const cache = primerCache(directory)

    return {
      event: async ({ event }) => {
        const id = event.properties?.info?.id
        if (!id) return
        if (event.type === "session.created") cache.get(id)
        else if (event.type === "session.deleted") cache.forget(id)
      },
      "experimental.chat.system.transform": async (request, output) => {
        if (!request.sessionID) return
        const text = await cache.get(request.sessionID)
        if (text && text.trim()) output.system.push(text.trim())
      },
    }
  },
}
