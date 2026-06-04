# Weft.app — native macOS surface

A thin native client over the Weft daemon (`localhost:7777`). Three things, done
the macOS way:

1. **Read** — a full-screen window that renders your `.html` vault notes
   beautifully. A `WKWebView` draws the note body under a tuned reader
   stylesheet; the sidebar, brain panel, and toolbar are native SwiftUI. Clicking
   an in-vault link navigates inside the app; external links open in your browser.
2. **Capture** — drop a thought from anywhere. Inline in the toolbar, from the
   menu-bar item, or via the global hotkey **⌃⌥Space** (a floating panel). Text
   is appended to today's daily note (`POST /api/capture`).
3. **Surface** — the brain panel shows what Weft would surface for the open note
   (trail · backlinks · activation-ranked neighbors with reason chips and the
   `✦ resurfaced` marker · on-this-day). And the daemon *pushes*: when it finds
   something worth resurfacing it streams a suggestion over SSE, which arrives as
   a native notification + a `✦` toolbar badge / inbox.

## Build & run

```sh
# 1. run the daemon (from the repo root)
make run                 # weft serve ~/notes

# 2. build + launch the app
make app-run             # or: ./macos/run.sh
#   make app             # just build → macos/build/Weft.app
```

Requirements: Xcode / Swift 5.9+, macOS 14+.

The app is packaged as a signed `.app` bundle (`macos/build/Weft.app`). The
ad-hoc signature + stable bundle id are what let notifications work; run the
bundle, not the bare binary.

## Proactive surfacing

The push channel is `GET /api/surface/stream` (Server-Sent Events) on the daemon.
A background ticker picks at most one suggestion per interval — preferring a
*resurfaced* (forgotten-yet-relevant) note, then an on-this-day anniversary, then
the strongest live association — and suppresses a note it pushed within the last
day. Cadence defaults to 5 minutes; for testing set it short:

```sh
WEFT_SURFACE_INTERVAL=20s make run
```

## Layout

```
macos/
├── Package.swift
├── build.sh / run.sh
└── Sources/Weft/
    ├── WeftApp.swift       @main, menu-bar extra, commands, capture panel
    ├── AppModel.swift      @Observable app state
    ├── WeftClient.swift    async HTTP client
    ├── SurfaceStream.swift SSE client (proactive surfacing)
    ├── Models.swift        Codable mirrors of the daemon JSON
    ├── RootView.swift      sidebar | reader | brain inspector
    ├── SidebarView.swift   foldered vault list + filter
    ├── ReaderView.swift    WKWebView + link interception
    ├── ReaderCSS.swift     the reader stylesheet
    ├── BrainPanel.swift    surfacing UI (trail/backlinks/surfaced/on-this-day)
    ├── CaptureView.swift   toolbar field, global panel, inbox, toast
    ├── Notifier.swift      UNUserNotificationCenter
    ├── HotkeyManager.swift global ⌃⌥Space hotkey (Carbon)
    └── Theme.swift         Weft color tokens (light/dark)
```
