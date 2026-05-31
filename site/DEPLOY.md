# Deploying tryweft.app

An [Astro](https://astro.build) site with [Starlight](https://starlight.astro.build)
for the docs. It builds to static files — no server, no secrets; it holds nothing
but the marketing pages, the docs, and the installer script. The binaries live on
object storage you own. This keeps the local-first + BYOC-E2EE architecture intact:
nothing we host ever sees a vault or a key.

```
site/
  src/pages/index.astro      landing (custom)
  src/content/docs/*.md      Starlight docs (search + nav for free)
  src/styles/                landing.css + Starlight theme.css
  public/install.sh          served at tryweft.app/install.sh
  public/{favicon.svg,robots.txt}
  astro.config.mjs
```

## 1. Build + host the site — Cloudflare Pages

You already use Cloudflare (R2), so Pages is the natural fit. Either deploy the
built output from the CLI:

```sh
cd site
npm install
npm run build                       # → site/dist/ (also builds the search index + sitemap)
npx wrangler pages deploy dist --project-name tryweft
```

…or connect the repo in the Pages dashboard with:

- **Root directory:** `site`
- **Build command:** `npm run build`
- **Build output directory:** `dist`

Then under Pages → tryweft → **Custom domains**, add `tryweft.app` (and `www`).
DNS is automatic if the domain is on Cloudflare; otherwise add the CNAME they show.

`public/install.sh` is copied to the site root, so
`curl -fsSL https://tryweft.app/install.sh | sh` works once deployed. Starlight
ships full-text search (Pagefind) and a sitemap with no extra config.

## 2. Host the binaries — R2 at dl.tryweft.app

The installer fetches `https://tryweft.app/dl/<version>/weft-<os>-<arch>` plus a
`SHA256SUMS`. Host these on R2 (no egress cost, no per-file size limit):

```sh
make release                 # from the repo root → dist/weft-* + dist/SHA256SUMS

# create a public R2 bucket once, then upload a versioned + a `latest` copy:
VER=v0.9.0
for f in dist/weft-* dist/SHA256SUMS; do
  wrangler r2 object put  "weft-dl/$VER/$(basename $f)"     --file "$f" --remote
  wrangler r2 object put  "weft-dl/latest/$(basename $f)"   --file "$f" --remote
done
```

Bind the bucket to a public hostname `dl.tryweft.app` (R2 → bucket → Settings →
Public access → custom domain), then make the default base resolve, either:

- set `WEFT_DL_BASE=https://dl.tryweft.app` in the install command, or
- redirect `tryweft.app/dl/*` → `dl.tryweft.app/*` — add `public/_redirects` with
  `/dl/* https://dl.tryweft.app/:splat 302` so the default base just works.

The default in `public/install.sh` is `https://tryweft.app/dl`; change it there if
you prefer a different host.

## 3. Verify

```sh
curl -fsSL https://tryweft.app/install.sh | sh    # installs latest
weft serve ~/notes
```

## Notes

- **`curl | sh` honesty:** the install page also shows the manual download +
  checksum path. The installer verifies SHA-256 against the published `SHA256SUMS`
  and refuses (or warns, never silently) when it can't.
- **No analytics, no trackers, no web fonts from a CDN** — the site is as
  local-first in spirit as the tool. Keep it that way.
- The Windows `.exe` is download-only (no one-line installer); the install page
  links it directly.
- Docs live at root slugs (`/getting-started`, `/sync`, …); the landing is the
  custom `/`. Add a page by dropping a `.md` in `src/content/docs/` and listing it
  in `astro.config.mjs`'s `sidebar`.
