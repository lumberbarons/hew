// @hew-managed — installed by `hew hooks install opencode`; remove with
// `hew hooks remove opencode`.
//
// Runs `hew prime` once per session and injects the cached output as system
// context, so the primer is present before the first turn and never renders
// as a chat message.
const primers = new Map()

const prime = async ($, worktree) =>
  $`hew prime`.cwd(worktree).text().catch(() => "")

export const HewPrimePlugin = async ({ $, worktree }) => {
  return {
    event: async ({ event }) => {
      if (event.type === "session.created") {
        const id = event.properties.info.id
        if (!primers.has(id)) primers.set(id, prime($, worktree))
      } else if (event.type === "session.deleted") {
        primers.delete(event.properties.info.id)
      }
    },
    "experimental.chat.system.transform": async (input, output) => {
      if (!input.sessionID) return
      let primer = primers.get(input.sessionID)
      if (primer === undefined) {
        primer = prime($, worktree)
        primers.set(input.sessionID, primer)
      }
      const text = await primer
      if (text && text.trim()) output.system.push(text.trim())
    },
  }
}
