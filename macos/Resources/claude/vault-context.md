---
description: Pull relevant context from my Weft vault before starting work
argument-hint: [topic — omit to infer from the current task/repo]
---

Pull prior context from my Weft vault (weft MCP tools) for: $ARGUMENTS

If no topic was given, infer it from the current conversation, repo, or working directory.

1. `search_notes` for the topic (try 2–3 query phrasings if the first returns nothing).
2. For the most relevant hit, call `surface_note` to get its backlinks and surfacing-ranked neighbors.
3. `read_note` the top 2–4 notes that look load-bearing.
4. Report back concisely: what I've already decided, learned, or tried on this topic; anything that contradicts or shortcuts the current task; and which notes you drew from (by path). If the vault has nothing, say so in one line and move on.

Do not write anything to the vault in this command.
