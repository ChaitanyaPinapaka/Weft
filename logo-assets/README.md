# Weft — Logo Assets

Master vectors live in this folder; rasterized PNGs are in `png/`.
Everything is flat vector, no gradients. Blue is only ever the one surfaced weft thread.

## Colors
| Token | Light | Dark |
|---|---|---|
| Accent (weft) | `#2563eb` | `#60a5fa` |
| Ink (warp / text) | `#1a1a1a` | `#e9e9e9` |
| Surface | `#fafafa` | `#1a1a1a` |
| Muted gray | `#888888` | `#888888` |

## What's here
- **weft-symbol-{light,dark}.svg** — the primary mark, transparent. Use anywhere you need just the symbol.
- **weft-glyph-{mono,black,white}.svg** — simplified 3-thread glyph for tiny sizes (menu bar). `mono` uses `currentColor`.
- **weft-appicon-{light,dark,blue}.svg** — the mark baked onto a rounded-rect tile for Dock / app icons.
- **weft-wordmark-{light,dark}.svg** — lowercase `weft` only.
- **weft-lockup-{light,dark}.svg** — symbol + wordmark, horizontal.
- **png/** — exports at 16–1024 px (symbol), 16/32/44 (glyph), 128–1024 (tiles), plus lockups.

## How to use each

### macOS Dock / app icon
Take `png/weft-appicon-light-1024.png` (or build all sizes from the SVG) and either:
- **Xcode:** drop the 1024 px PNG into the App Icon slot of your asset catalog — it generates the rest.
- **Hand-rolled `.icns`:** put the sized PNGs in a `weft.iconset/` folder named `icon_16x16.png`, `icon_16x16@2x.png` … `icon_512x512@2x.png`, then run `iconutil -c icns weft.iconset`.

macOS already applies the squircle mask + shadow, so you can also use the **bare symbol** on a solid `#fafafa` / `#1a1a1a` square if you'd rather not bake in the corner radius.

### macOS menu-bar glyph
Use **weft-glyph** as a *template image* so macOS tints it for light/dark and selection automatically:
- Name the file `weftTemplate.png` (the `Template` suffix matters) and set `image.isTemplate = true` (or `NSImage.template`).
- Provide `weftTemplate.png` (16 px) and `weftTemplate@2x.png` (32 px) from `png/weft-glyph-black-16.png` / `-32.png`.
- The black fill is fine — template images are recolored by the system; only the alpha is used.

### Favicon / web
```html
<link rel="icon" type="image/svg+xml" href="weft-symbol-light.svg">
<link rel="icon" type="image/png" sizes="32x32" href="png/weft-symbol-light-32.png">
<link rel="apple-touch-icon" href="png/weft-appicon-light-512.png">
```
Swap to the `-dark` symbol inside a `prefers-color-scheme: dark` media query.

### Headers / docs / marketing
Use **weft-lockup-light.svg** on light surfaces, **weft-lockup-dark.svg** on dark. The wordmark is system/SF — if you need it in another tool, set lowercase `weft`, weight ~600, letter-spacing −3.5%.

## Clear space & minimum size
- Keep clear space ≥ the height of one warp thread gap around the mark.
- Symbol: don't go below 16 px — below that use the menu-bar glyph instead.
- Never recolor the warp blue or the weft ink; never add a glow, gradient, or outline.
