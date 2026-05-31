# Deploying tryweft.app

The site is static files in `site/` — no build step, no server, no secrets. It
holds nothing but HTML/CSS and the installer script; the binaries live on object
storage. This keeps the local-first + BYOC-E2EE architecture intact: nothing we
host ever sees a vault or a key.

```
site/
  index.html          landing
  install.html        download / install page
  docs/*.html         documentation
  styles.css
  install.sh          served at tryweft.app/install.sh
```

## 1. Host the site — Cloudflare Pages

You already use Cloudflare (R2), so Pages is the natural fit: free static
hosting, custom domain, deploy from the CLI.

```sh
npm i -g wrangler            # one-time
wrangler login
wrangler pages deploy site --project-name tryweft
```

Then in the Cloudflare dashboard → Pages → tryweft → **Custom domains**, add
`tryweft.app` (and `www`). DNS is automatic if the domain is on Cloudflare; if it
isn't, move the nameservers to Cloudflare or add the CNAME they show you.

`install.sh` is served verbatim at `https://tryweft.app/install.sh` (Pages serves
any file as-is), so `curl -fsSL https://tryweft.app/install.sh | sh` works once
deployed.

> GitHub Pages / Netlify work too — point them at `site/` as the publish dir.

## 2. Host the binaries — R2 at dl.tryweft.app

The installer fetches `https://tryweft.app/dl/<version>/weft-<os>-<arch>` plus a
`SHA256SUMS`. Host these on R2 (no egress cost, no per-file size limit):

```sh
make release                 # → dist/weft-* + dist/SHA256SUMS

# create a public R2 bucket once, then upload a versioned + a `latest` copy:
VER=v0.9.0
for f in dist/weft-* dist/SHA256SUMS; do
  wrangler r2 object put  "weft-dl/$VER/$(basename $f)"     --file "$f" --remote
  wrangler r2 object put  "weft-dl/latest/$(basename $f)"   --file "$f" --remote
done
```

Bind the bucket to a public hostname `dl.tryweft.app` (R2 → bucket → Settings →
Public access → custom domain), then point the installer's base at it:

- either set `WEFT_DL_BASE=https://dl.tryweft.app` in the install command, or
- redirect `tryweft.app/dl/*` → `dl.tryweft.app/*` (a Pages `_redirects` line:
  `/dl/* https://dl.tryweft.app/:splat 302`) so the default base just works.

The default in `install.sh` is `https://tryweft.app/dl`; change it there if you
prefer a different host.

## 3. Verify

```sh
curl -fsSL https://tryweft.app/install.sh | sh    # installs latest
weft serve ~/notes
```

## Notes

- **`curl | sh` honesty:** the page also shows the manual download + checksum
  path for anyone who (reasonably) won't pipe a script to a shell. The installer
  verifies SHA-256 against the published `SHA256SUMS` before installing.
- **No analytics, no trackers, no fonts from a CDN** — the site is as local-first
  in spirit as the tool. Keep it that way.
- The Windows `.exe` is download-only (no one-line installer); the install page
  links it directly.
