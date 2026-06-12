---
description: Distill this session's durable outcome into a Weft vault note
argument-hint: [optional focus or note title]
---

Write what this session produced into my Weft vault (weft MCP tools). Focus, if given: $ARGUMENTS

1. Distill the session to its durable core: the decision made and why, the thing learned, the result obtained. Skip process narration — the note is for me in six months, not a transcript.
2. Pick the destination:
   - A concept or insight → new note in `learnings/`, short-kebab-case filename.
   - Project progress or a decision → the matching `projects/<name>.html` if one exists (`list_notes` to check), else a new one.
   - Trivia not worth its own note → append to today's `daily/YYYY-MM-DD.html`.
3. **If the note exists, `read_note` first** and write back the full content with your addition — `write_note` overwrites.
4. Follow vault conventions: HTML fragment, `<h1>` title (or `<h2>` section when appending), semantic HTML, `[[wikilinks]]` to related notes (`search_notes` to find link targets), italic provenance line: `<p><em>Logged YYYY-MM-DD from a Claude Code session.</em></p>`.
5. Confirm with the note path and a one-line summary of what you wrote. If nothing in the session is worth keeping, say so instead of writing filler.
