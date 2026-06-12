import SwiftUI
import WeftKit

// "Set up this Mac": five rows, each a live status + one idempotent action.
// Presented as a sheet — from the Note menu, or automatically at boot when the
// daemon is unreachable and no LaunchAgent is installed.
struct SetupView: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(AppModel.self) private var model
    @State private var setup = SetupModel()

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider().overlay(Weft.border)
            VStack(spacing: 4) {
                cliRow
                daemonRow
                extensionRow
                mcpRow
                memoryRow
            }
            .padding(.horizontal, 18).padding(.vertical, 10)
            Divider().overlay(Weft.border)
            footer
        }
        .frame(width: 560)
        .background(Weft.bg)
        .task { await setup.refreshAll() }
    }

    private var header: some View {
        HStack(spacing: 8) {
            Image(systemName: "circle.lefthalf.filled").foregroundStyle(Weft.accent)
            VStack(alignment: .leading, spacing: 2) {
                Text("Set Up This Mac").font(.system(size: 14, weight: .semibold)).foregroundStyle(Weft.text)
                Text("CLI · daemon · Chrome extension · Claude Code")
                    .font(.weftMono).foregroundStyle(Weft.muted)
            }
            Spacer()
            Button("Done") { dismiss() }
                .buttonStyle(.plain).font(.system(size: 12, weight: .medium))
                .foregroundStyle(Weft.accent)
        }
        .padding(.horizontal, 18).padding(.vertical, 14)
    }

    private var footer: some View {
        HStack(spacing: 8) {
            if let err = setup.lastError {
                Text(err).font(.weftMono).foregroundStyle(Weft.danger)
                    .lineLimit(2).textSelection(.enabled)
            }
            Spacer()
            // The next surface after this Mac is set up: SAS-pair an iPhone.
            Button("Add a Device…") {
                dismiss()
                // Presenting while this sheet is still animating out gets
                // dropped — give the dismissal a beat first.
                Task {
                    try? await Task.sleep(for: .milliseconds(350))
                    model.showAddDevice = true
                }
            }
            .buttonStyle(.plain).font(.system(size: 12)).foregroundStyle(Weft.accent)
            Button("Re-check") { Task { await setup.refreshAll() } }
                .buttonStyle(.plain).font(.system(size: 12)).foregroundStyle(Weft.muted)
                .disabled(setup.busyRow != nil)
        }
        .padding(.horizontal, 18).padding(.vertical, 10)
    }

    // MARK: - Rows

    private var cliRow: some View {
        SetupRowView(
            title: "weft CLI",
            status: setup.cli,
            busy: setup.busyRow == .cli,
            anyBusy: setup.busyRow != nil,
            actionLabel: {
                if case .attention = setup.cli { return "Update" }
                if case .done = setup.cli { return "Reinstall" }
                return "Install"
            }(),
            action: { setup.installCLI() }
        )
    }

    private var daemonRow: some View {
        SetupRowView(
            title: "Daemon at login",
            status: setup.daemon,
            busy: setup.busyRow == .daemon,
            anyBusy: setup.busyRow != nil,
            actionLabel: "Install Agent",
            action: { setup.installDaemon() }
        ) {
            VStack(alignment: .leading, spacing: 6) {
                HStack(spacing: 6) {
                    Text("vault").font(.weftLabel).foregroundStyle(Weft.muted)
                    TextField("~/Vault", text: Bindable(setup).vaultPath)
                        .textFieldStyle(.plain).font(.weftMono).foregroundStyle(Weft.text)
                        .padding(.horizontal, 7).padding(.vertical, 3)
                        .background(RoundedRectangle(cornerRadius: 5).fill(Weft.surface))
                        .frame(width: 220)
                }
                if case .attention = setup.daemon {
                    Text("A daemon is answering on :7777 but launchd doesn't manage it — probably a terminal. Installing the agent kills nothing: stop that process yourself and launchd takes over.")
                        .font(.system(size: 11)).foregroundStyle(Weft.muted)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
        }
    }

    private var extensionRow: some View {
        SetupRowView(
            title: "Chrome extension",
            status: setup.ext,
            busy: setup.busyRow == .ext,
            anyBusy: setup.busyRow != nil,
            actionLabel: "Copy + Open Chrome",
            action: { setup.installExtension() }
        )
        .popover(isPresented: Bindable(setup).showChromeSteps, arrowEdge: .trailing) {
            chromeSteps
        }
    }

    private var chromeSteps: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("FINISH IN CHROME")
                .font(.weftLabel).tracking(0.8).foregroundStyle(Weft.muted)
            Text("1.  Toggle **Developer mode** (top right).")
            Text("2.  Click **Load unpacked**, press ⌘⇧G, paste — the path is on your clipboard.")
        }
        .font(.system(size: 12)).foregroundStyle(Weft.text)
        .padding(14).frame(width: 300)
        .background(Weft.bg)
    }

    private var mcpRow: some View {
        SetupRowView(
            title: "Claude Code MCP",
            status: setup.mcp,
            busy: setup.busyRow == .mcp,
            anyBusy: setup.busyRow != nil,
            actionLabel: "Register",
            action: { setup.installMCP() }
        )
    }

    private var memoryRow: some View {
        SetupRowView(
            title: "Claude memory",
            status: setup.memory,
            busy: setup.busyRow == .memory,
            anyBusy: setup.busyRow != nil,
            actionLabel: "Wire Up",
            action: { setup.installMemory() }
        )
    }
}

// One setup row: status dot + title + detail line, action button, optional
// extra content (vault field, take-over explanation) below.
private struct SetupRowView<Extra: View>: View {
    let title: String
    let status: SetupStatus
    let busy: Bool
    let anyBusy: Bool
    let actionLabel: String
    let action: () -> Void
    @ViewBuilder var extra: Extra

    init(
        title: String, status: SetupStatus, busy: Bool, anyBusy: Bool,
        actionLabel: String, action: @escaping () -> Void,
        @ViewBuilder extra: () -> Extra = { EmptyView() }
    ) {
        self.title = title
        self.status = status
        self.busy = busy
        self.anyBusy = anyBusy
        self.actionLabel = actionLabel
        self.action = action
        self.extra = extra()
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 10) {
                Circle().fill(dotColor).frame(width: 7, height: 7)
                VStack(alignment: .leading, spacing: 2) {
                    Text(title).font(.system(size: 13, weight: .medium)).foregroundStyle(Weft.text)
                    Text(detail).font(.weftMono).foregroundStyle(Weft.muted).lineLimit(2)
                }
                Spacer(minLength: 12)
                if busy {
                    ProgressView().controlSize(.small)
                } else {
                    Button(actionLabel, action: action)
                        .font(.system(size: 12))
                        .disabled(disabled)
                }
            }
            extra.padding(.leading, 17)
        }
        .padding(.horizontal, 12).padding(.vertical, 10)
        .background(
            RoundedRectangle(cornerRadius: Weft.radius)
                .fill(Weft.surface)
                .overlay(RoundedRectangle(cornerRadius: Weft.radius).strokeBorder(Weft.border))
        )
    }

    private var disabled: Bool {
        if anyBusy { return true }
        switch status {
        case .blocked, .checking: return true
        default: return false
        }
    }

    private var detail: String {
        switch status {
        case .checking: return "checking…"
        case .missing(let s), .attention(let s), .done(let s), .blocked(let s): return s
        }
    }

    private var dotColor: Color {
        switch status {
        case .checking, .blocked: return Weft.muted
        case .missing: return Weft.danger
        case .attention: return .orange
        case .done: return .green
        }
    }
}
