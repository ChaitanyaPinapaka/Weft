import SwiftUI
import AppKit
import WeftKit

@main
struct WeftApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @State private var model = AppModel.shared

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(model)
                .frame(minWidth: 920, minHeight: 600)
        }
        .windowStyle(.titleBar)
        .windowToolbarStyle(.unified)
        .commands {
            CommandGroup(replacing: .newItem) {} // no "New" — capture is the verb
            CommandMenu("Note") {
                Button("Today's Daily") { Task { await model.openDaily() } }
                    .keyboardShortcut("t", modifiers: .command)
                Button("Quick Capture…") { delegate.toggleCapturePanel() }
                    .keyboardShortcut("n", modifiers: .command)
                Button("Graph") { model.toggleGraph() }
                    .keyboardShortcut("g", modifiers: [.command, .shift])
                Divider()
                Button("Set Up This Mac…") { model.showSetup = true }
                Button("Add a Device…") { model.showAddDevice = true }
            }
        }

        // The vault graph lives in the main window's detail pane (toggled by
        // model.showGraph), not a separate scene — clicking a node drops you
        // straight onto that note in the same reader.
        //
        // The menu-bar item is an AppKit NSStatusItem (see AppDelegate), not a
        // SwiftUI MenuBarExtra: MenuBarExtra(image:) only resolves names from a
        // compiled asset catalog, but NSImage(named:) finds our loose template
        // PNG, so the woven glyph renders reliably.
    }
}

// The menu-bar dropdown: capture from anywhere plus a peek at what's surfaced.
struct MenuBarView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            CapturePanelView(onFinish: {})
            Divider().overlay(Weft.border)
            SurfaceInboxView()
            Divider().overlay(Weft.border)
            HStack {
                Button("Open Weft") {
                    NSApp.activate(ignoringOtherApps: true)
                    NSApp.windows.first { $0.canBecomeMain }?.makeKeyAndOrderFront(nil)
                }
                .buttonStyle(.plain).font(.system(size: 12, weight: .medium))
                .foregroundStyle(Weft.accent)
                Spacer()
                Button("Quit") { NSApp.terminate(nil) }
                    .buttonStyle(.plain).font(.system(size: 12)).foregroundStyle(Weft.muted)
            }
            .padding(.horizontal, 14).padding(.vertical, 10)
        }
        .frame(width: 420)
        .background(Weft.bg)
    }
}

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    private let hotkey = HotkeyManager()
    private var capturePanel: NSPanel?
    private var statusItem: NSStatusItem?
    private let menuPopover = NSPopover()

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.regular)
        Notifier.shared.configure()
        AppModel.shared.bootstrap()
        hotkey.onTrigger = { [weak self] in self?.toggleCapturePanel() }
        hotkey.register()
        setupStatusItem()
    }

    // Flush the embedded editor's pending save before quitting (Cmd-Q / Quit).
    // WKWebView fires no beforeunload on teardown, so without this the last ~1s
    // of edits would be lost on exit. terminateLater lets the async flush finish.
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        guard AppModel.shared.editing else { return .terminateNow }
        Task { @MainActor in
            await AppModel.shared.flushOnExit()
            NSApp.reply(toApplicationShouldTerminate: true)
        }
        return .terminateLater
    }

    // MARK: - Menu-bar item

    private func setupStatusItem() {
        let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        if let button = item.button {
            if let glyph = NSImage(named: "weftTemplate") {
                glyph.isTemplate = true
                glyph.size = NSSize(width: 18, height: 18)
                button.image = glyph
                WeftLog.write("statusItem glyph loaded ok")
            } else {
                button.title = "weft"
                WeftLog.write("statusItem glyph MISSING — fell back to text")
            }
            button.action = #selector(toggleMenuPopover(_:))
            button.target = self
        }
        statusItem = item

        menuPopover.behavior = .transient
        let host = NSHostingController(rootView: MenuBarView().environment(AppModel.shared))
        host.sizingOptions = [.preferredContentSize] // popover tracks SwiftUI size
        menuPopover.contentViewController = host
    }

    @objc private func toggleMenuPopover(_ sender: Any?) {
        guard let button = statusItem?.button else { return }
        if menuPopover.isShown {
            menuPopover.performClose(sender)
        } else {
            NSApp.activate(ignoringOtherApps: true)
            menuPopover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY)
            menuPopover.contentViewController?.view.window?.makeKey()
        }
    }

    // Summon (or dismiss) the floating quick-capture panel, focused and centered.
    func toggleCapturePanel() {
        if let panel = capturePanel, panel.isVisible {
            closeCapturePanel()
            return
        }
        let panel = capturePanel ?? makeCapturePanel()
        capturePanel = panel
        panel.center()
        NSApp.activate(ignoringOtherApps: true)
        panel.makeKeyAndOrderFront(nil)
    }

    private func makeCapturePanel() -> NSPanel {
        let panel = CapturePanel(
            contentRect: NSRect(x: 0, y: 0, width: 420, height: 150),
            styleMask: [.borderless, .nonactivatingPanel],
            backing: .buffered, defer: false
        )
        panel.level = .floating
        panel.isOpaque = false
        panel.backgroundColor = .clear
        panel.hasShadow = true
        panel.isMovableByWindowBackground = true
        panel.hidesOnDeactivate = false

        let host = NSHostingView(
            rootView: CapturePanelView(onFinish: { [weak self] in self?.closeCapturePanel() })
                .environment(AppModel.shared)
        )
        host.wantsLayer = true
        host.layer?.cornerRadius = 12
        host.layer?.masksToBounds = true
        panel.contentView = host
        return panel
    }

    private func closeCapturePanel() {
        capturePanel?.orderOut(nil)
    }
}

// Borderless panels can't become key by default; this lets the capture field
// receive text the moment the panel appears.
final class CapturePanel: NSPanel {
    override var canBecomeKey: Bool { true }
    override var canBecomeMain: Bool { true }
}
