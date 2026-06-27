---
title: "Concepts"
description: "How Weft thinks: HTML files you own, surfacing over searching, and an ACT-R memory-activation model that ranks what your brain would recall right now."
---

Weft is built on two ideas: your notes are real HTML files you own, and the way you find them again is surfacing, not searching. This page explains the model behind both.

## HTML is canonical

Your vault is a folder of `.html` files on disk. Sub-folders are allowed. That is the whole storage format — there is no database of record, no proprietary container, no markdown intermediate that gets converted on the way in or out. What you write is what is on disk.

This has consequences worth being explicit about:

- **You own the files.** Delete Weft and your notes are still there, openable in any browser. No accounts, no lock-in.
- **Notes are publishable as-is.** HTML is already a web page, so clipping a page is essentially a copy, and a note can be served without a build step.
- **Nothing is ever deleted by Weft.** Dormant notes rank lower in surfacing; they are never removed. (When sync removes a file, it moves to a `.trash` folder rather than vanishing.)

:::note[Why HTML over markdown?]
HTML is the native format of the web and of the things you clip, and it carries rich structure cleanly. A markdown layer would mean a lossy round-trip on every save. Weft refuses that trade.
:::

You still get a fast way to write it. The editor surfaces `[[wikilinks]]` for connecting notes and a `/` slash-command menu for inserting structure, and saves plain HTML underneath. The wikilinks you type become the `<a href>` links the index parses for backlinks — so authoring and surfacing are the same act seen from two ends.

## The index is derived, never authored

Everything Weft needs to be fast lives in a SQLite index built *from* the HTML — never the other way around. It holds an FTS5 full-text table for lexical search, an embeddings blob for semantic similarity, and backlinks parsed from `<a href>` on each save.

The index is disposable. Each device rebuilds it from the `.html` files, so it is never synced and never the source of truth. If it is lost, it regenerates. Your notes are the data; the index is a cache.

## Surfacing, not searching

A search bar makes you supply the cue. Weft does the opposite: open any note and the **brain panel** shows what your brain would surface right now, given where you are. It draws on four kinds of association:

| Signal | What it surfaces |
| --- | --- |
| Backlinks | Notes that link to the one you are reading, parsed from `<a href>`. |
| Semantic neighbors | Notes that are *about* the same thing, by embedding similarity — even with no shared words or links. |
| Co-accessed | Notes you have recently opened around the same time as this one. |
| This day in past years | Notes from this calendar date in prior years. |

The point is cue-driven, associative recall — the closest software analog to how memory actually works. You arrive somewhere and the related things come to you.

## The ranking model: ACT-R memory activation

What surfaces, and in what order, is decided by a memory-activation model borrowed from ACT-R, the cognitive architecture. Each note has an **activation** score with two parts:

- **Base-level activation** — from recency and frequency of access. Notes decay like memory: the longer since you touched one, the lower it ranks. This is the dormancy signal, and it only affects ranking, never retention.
- **Spreading activation** — activation flows outward from your current context across links and associations, so notes connected to where you are get a lift.

The result is a list that reflects both how memorable a note is on its own and how related it is to the moment. Because activation decays, the panel stays current instead of accreting everything you have ever written.

:::note[Explain why.]
The brain panel has an **explain** toggle: turn it on and each surfaced note shows the signals that put it there — the backlink, the similarity, the co-access, the date. A separate tuning page lets you adjust the weights behind the model if the defaults do not match how you think.
:::

## The model learns from you

Surfacing is not static. Two loops keep it tuned to how you actually think:

- **Reinforcement.** Follow a surfaced suggestion and that association strengthens; one you keep ignoring decays back to neutral. The panel learns which connections earn your attention — from your own clicks, not a fixed weighting.
- **The Weaver.** A pass (`weft weave`) scans for notes that are semantically close but never linked and writes the pairs to a dated digest as proposed `[[wikilinks]]`. The vault tends itself: you confirm the connections that are real, it never edits your notes.

## The graph: the same model, seen whole

The brain panel shows associations one note at a time. The **graph view** shows them all at once — a map of the vault that draws on the same signals. Each note is a node; the layout is the shape of how your notes connect.

- **Folder clusters** — nodes are coloured by the folder they live in, so the vault's structure reads at a glance.
- **Two kinds of edge** — solid edges are backlinks parsed from `<a href>`; fainter edges are semantic-similarity links from the embeddings. A note can be tied to another by a link you wrote or by what it is about, and the graph distinguishes the two.
- **Activation sizing** — each node is sized by its activation (the same recency-plus-frequency base level from the ranking model), so the notes most alive in your memory stand out.

Hover a node to highlight its neighbourhood, read the legend for the encoding, and use the tag and minimum-link filters and minimap to navigate a dense vault. It is the surfacing model made visible: the same backlinks, semantic neighbours, and activation, laid out as a brain map rather than a ranked list.

## What works in the prebuilt binary

One distinction matters. The one-line-installer binary ships a **stub embedder**, so semantic neighbors are the one signal it does not compute. Everything else in the model — backlinks, co-access, this-day, recency and frequency decay, spreading activation — works fully out of the box.

Semantic neighbors are an opt-in upgrade you get by building from source. `make build-ort` swaps in a local CPU embedding model (all-MiniLM-L6-v2, 384-dimensional) via ONNX. It runs entirely on your machine — no API, nothing leaves the device.

```sh
# default build: every signal except semantic neighbors
make build

# add local CPU embeddings for semantic neighbors
make build-ort
```

See [Install](/install) for the from-source steps.

## Search is the fallback, not the front door

Full-text search exists — it is the FTS5 table in the index — but it is deliberately not the primary interface. Surfacing is how you find things you were not explicitly looking for; search is there for the times you know exactly the word you want and just need to jump to it.

## In short

- **Files you own** — Plain `.html` in a folder. No markdown layer, no lock-in, nothing ever deleted by the system.
- **A derived index** — SQLite with FTS5, embeddings, and parsed backlinks — rebuilt from the HTML, never the source of truth.
- **Surfacing over search** — The brain panel shows backlinks, semantic neighbors, co-accessed notes, and this-day-in-past-years; the graph view maps the same signals whole.
- **Memory-style ranking** — ACT-R activation: recency and frequency decay plus spreading activation, with an explain toggle.

[Getting started](/getting-started)
[Install](/install)
