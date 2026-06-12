<!-- weft:memory:begin -->
## Weft vault (personal knowledge base)

I keep a personal vault at `~/Vault`, served by Weft and reachable in every session via the `weft` MCP tools (`list_notes`, `read_note`, `search_notes`, `surface_note`, `write_note`).

**Pull context at the start of substantive work.** When a task involves a topic I may have prior notes on (my projects, past decisions, things I'm learning — especially AI infra, Weft itself, blog drafts), run `search_notes` for it and `surface_note` on the best hit before diving in. Mention what you found in one line. Skip this for trivial or purely mechanical tasks.

**Write back what we learn.** When a session lands something durable — an architecture decision, a debugging discovery, a concept I finally understood, a result worth keeping — offer to save it to the vault (or just do it if I ask). Use `/vault-log` conventions:

- Notes are **HTML fragments**: `<h1>` title, then semantic HTML (`<p>`, `<ul>`, `<h2>`, `<strong>`, `<code>`). No markdown, no full `<html>` document needed.
- Link related notes inline with `[[wikilink]]` syntax (the target is the note's filename without `.html`, e.g. `[[ai-infra-journey]]`).
- File by kind: `learnings/` for concepts and insights, `projects/<name>.html` for project decisions and progress, `daily/YYYY-MM-DD.html` for ephemera.
- End with an italic provenance line, e.g. `<p><em>Logged 2026-06-09 from a Claude Code session.</em></p>`.
- **`write_note` overwrites.** Before modifying an existing note, `read_note` it first and write back the full updated content. When in doubt, create a new note and wikilink it rather than rewriting an old one.
- Notes are never deleted. Don't ask to delete; mark things superseded instead.
<!-- weft:memory:end -->
