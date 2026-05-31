---
title: "Import"
description: "Bring existing notes into your Weft vault from markdown, Notion, browser bookmarks, or Apple Notes. Imported notes become plain .html files."
---

Bring your existing notes into the vault. Whatever you import becomes a plain `.html` file on disk — the same canonical format as everything else in Weft.

The command is:

```sh
weft import <kind> <src>
```

`<kind>` is one of `markdown`, `notion`, `bookmarks`, or `apple-notes`. `<src>` is the path to the file or folder you are importing from.

:::note[Nothing is deleted]
Import only adds notes to the vault. Existing notes are left alone unless you pass `--force` (see below).
:::

## Flags

| Flag | Effect |
| --- | --- |
| `--force` | Overwrite a note in the vault when an imported note maps to the same path. Without it, existing notes are kept and the incoming one is skipped. |
| `-v <vault>` | Target a specific vault path instead of the default. |

## Markdown

Import a folder of `.md` files. Each one is converted to a note in the vault.

```sh
weft import markdown ~/old-notes
```

## Notion

Point at a Notion workspace or page export (the folder or zip you get from Notion's "Export" option).

```sh
weft import notion ~/Downloads/notion-export
```

## Bookmarks

Import a browser bookmarks file — the HTML export every major browser produces from its bookmark manager.

```sh
weft import bookmarks ~/Downloads/bookmarks.html
```

## Apple Notes

Import notes exported from Apple Notes.

```sh
weft import apple-notes ~/Downloads/apple-notes-export
```

## After importing

Imported notes are indexed like any other: backlinks are parsed from their `<a href>` links on save, they appear in full-text search, and they start participating in surfacing. Open one and the brain panel will begin weaving it together with the rest of your vault as you use it.

:::note[Re-running an import?]
Run it again to pick up new source notes — existing vault notes stay untouched. Add `--force` only when you want the import to replace notes that already exist.
:::
