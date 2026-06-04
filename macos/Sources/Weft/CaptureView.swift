import SwiftUI
import WeftKit

// Inline toolbar capture: type a thought, ⏎ appends it to today's daily note.
// "Drop whatever I feel like" without leaving the note you're reading.
struct CaptureField: View {
    @Environment(AppModel.self) private var model
    @State private var text = ""
    @State private var justCaptured = false

    var body: some View {
        HStack(spacing: 6) {
            Image(systemName: justCaptured ? "checkmark.circle.fill" : "plus.circle")
                .font(.system(size: 12))
                .foregroundStyle(justCaptured ? Color.green : Weft.muted)
            TextField("capture a thought…", text: $text)
                .textFieldStyle(.plain)
                .font(.system(size: 13))
                .frame(width: 240)
                .onSubmit(submit)
        }
        .padding(.horizontal, 9).padding(.vertical, 5)
        .background(
            RoundedRectangle(cornerRadius: Weft.radius)
                .fill(Weft.surface)
                .overlay(RoundedRectangle(cornerRadius: Weft.radius).strokeBorder(Weft.border))
        )
    }

    private func submit() {
        let t = text
        guard !t.trimmingCharacters(in: .whitespaces).isEmpty else { return }
        model.capture(t)
        text = ""
        withAnimation { justCaptured = true }
        Task {
            try? await Task.sleep(nanoseconds: 1_400_000_000)
            withAnimation { justCaptured = false }
        }
    }
}

// The global quick-capture surface (summoned by the menu-bar item or hotkey),
// hosted in a floating panel so you can capture from anywhere.
struct CapturePanelView: View {
    @Environment(AppModel.self) private var model
    @State private var text = ""
    @FocusState private var focused: Bool
    var onFinish: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 7) {
                Image(systemName: "circle.lefthalf.filled").foregroundStyle(Weft.accent)
                Text("Capture to Weft").font(.system(size: 13, weight: .semibold))
                    .foregroundStyle(Weft.text)
                Spacer()
            }
            TextField("what's on your mind…", text: $text, axis: .vertical)
                .textFieldStyle(.plain)
                .font(.system(size: 15))
                .lineLimit(2...6)
                .focused($focused)
                .onSubmit(submit)
            HStack {
                Text("⏎ capture · esc dismiss")
                    .font(.weftMono).foregroundStyle(Weft.muted)
                Spacer()
            }
        }
        .padding(18)
        .frame(width: 420)
        .background(Weft.bg)
        .onExitCommand(perform: onFinish)
        .onAppear { focused = true }
    }

    private func submit() {
        model.capture(text)
        text = ""
        onFinish()
    }
}

// Popover listing recent proactive surfacings (the ✦ toolbar button's content).
struct SurfaceInboxView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Text("SURFACED FOR YOU")
                .font(.weftLabel).tracking(0.8).foregroundStyle(Weft.muted)
                .padding(.horizontal, 14).padding(.top, 14).padding(.bottom, 8)

            if model.inbox.isEmpty {
                Text("nothing surfaced yet")
                    .font(.system(size: 12)).foregroundStyle(Weft.muted)
                    .padding(.horizontal, 14).padding(.bottom, 14)
            } else {
                ForEach(model.inbox) { ev in
                    Button { model.openFromInbox(ev) } label: {
                        VStack(alignment: .leading, spacing: 3) {
                            HStack(spacing: 5) {
                                if ev.reason == "resurfaced" {
                                    Text("✦").font(.system(size: 11)).foregroundStyle(Weft.accent)
                                }
                                Text(ev.displayTitle)
                                    .font(.system(size: 13, weight: .medium))
                                    .foregroundStyle(Weft.text).lineLimit(1)
                            }
                            Text([ev.reason.map(reasonLabel), ev.detail].compactMap { $0 }.joined(separator: " · "))
                                .font(.weftMono).foregroundStyle(Weft.muted).lineLimit(1)
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal, 14).padding(.vertical, 7)
                    }
                    .buttonStyle(.plain)
                }
            }
        }
        .frame(width: 300)
        .background(Weft.bg)
        .onAppear { model.markInboxSeen() }
    }
}

// Transient in-window card shown when a push arrives while Weft is focused.
struct ToastView: View {
    let event: SurfaceEvent
    let onOpen: () -> Void
    let onDismiss: () -> Void

    var body: some View {
        HStack(spacing: 10) {
            Text("✦").foregroundStyle(Weft.accent)
            VStack(alignment: .leading, spacing: 2) {
                Text(event.reason.map(reasonLabel) ?? "surfaced")
                    .font(.weftLabel).tracking(0.6).foregroundStyle(Weft.muted)
                Text(event.displayTitle)
                    .font(.system(size: 13, weight: .medium))
                    .foregroundStyle(Weft.text).lineLimit(1)
            }
            Spacer(minLength: 12)
            Button("Open", action: onOpen)
                .buttonStyle(.plain).font(.system(size: 12, weight: .medium))
                .foregroundStyle(Weft.accent)
            Button { onDismiss() } label: {
                Image(systemName: "xmark").font(.system(size: 10)).foregroundStyle(Weft.muted)
            }
            .buttonStyle(.plain)
        }
        .padding(.horizontal, 14).padding(.vertical, 11)
        .frame(width: 340)
        .background(
            RoundedRectangle(cornerRadius: 10)
                .fill(Weft.surface)
                .shadow(color: .black.opacity(0.18), radius: 16, y: 6)
                .overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(Weft.border))
        )
    }
}
