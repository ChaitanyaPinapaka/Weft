import SwiftUI
import WeftKit

// Left rail: a browsable, foldered list of the vault. A quiet local filter (not
// a global search — surfacing is still the way you find things), a Today shortcut
// to the daily note, and a connection footer. Selection drives model.open().
struct SidebarView: View {
    @Environment(AppModel.self) private var model
    @State private var query = ""

    private var filtered: [NoteRef] {
        guard !query.isEmpty else { return [] }
        let q = query.lowercased()
        return model.notes.filter {
            $0.path.lowercased().contains(q) || $0.title.lowercased().contains(q)
        }
    }

    var body: some View {
        VStack(spacing: 0) {
            todayBar
            Divider().overlay(Weft.border)
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 2) {
                    if query.isEmpty {
                        ForEach(model.groupedNotes, id: \.folder) { group in
                            sectionLabel(group.folder.isEmpty ? "vault" : group.folder)
                            ForEach(group.notes) { row($0) }
                        }
                    } else {
                        sectionLabel("\(filtered.count) match\(filtered.count == 1 ? "" : "es")")
                        ForEach(filtered) { row($0) }
                    }
                }
                .padding(8)
            }
            Divider().overlay(Weft.border)
            statusFooter
        }
        .background(Weft.bg)
        .frame(minWidth: 220)
    }

    private var todayBar: some View {
        HStack(spacing: 8) {
            Button {
                Task { await model.openDaily() }
            } label: {
                Label("Today", systemImage: "sun.max")
                    .font(.system(size: 12, weight: .medium))
            }
            .buttonStyle(.plain)
            .foregroundStyle(Weft.accent)

            Spacer(minLength: 8)

            HStack(spacing: 5) {
                Image(systemName: "line.3.horizontal.decrease")
                    .font(.system(size: 10)).foregroundStyle(Weft.muted)
                TextField("filter", text: $query)
                    .textFieldStyle(.plain)
                    .font(.system(size: 12))
                    .frame(width: 90)
            }
            .padding(.horizontal, 8).padding(.vertical, 4)
            .background(RoundedRectangle(cornerRadius: 5).fill(Weft.surface))
        }
        .padding(.horizontal, 12).padding(.vertical, 9)
    }

    private func row(_ note: NoteRef) -> some View {
        let isCurrent = note.path == model.currentPath
        return Button { model.open(path: note.path) } label: {
            Text(note.title)
                .font(.system(size: 13, weight: isCurrent ? .semibold : .regular))
                .foregroundStyle(isCurrent ? Weft.accent : Weft.text)
                .lineLimit(1)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 8).padding(.vertical, 5)
                .background(
                    RoundedRectangle(cornerRadius: 5)
                        .fill(isCurrent ? Weft.accent.opacity(0.12) : .clear)
                )
        }
        .buttonStyle(.plain)
        .contextMenu {
            // Soft remove — RootView's alert confirms with the never-delete wording.
            Button("Remove (moves to .trash/)", role: .destructive) {
                model.requestTrash(note.path)
            }
        }
    }

    private func sectionLabel(_ text: String) -> some View {
        Text(text.uppercased())
            .font(.weftLabel).tracking(0.8)
            .foregroundStyle(Weft.muted)
            .padding(.horizontal, 8).padding(.top, 12).padding(.bottom, 4)
            .frame(maxWidth: .infinity, alignment: .leading)
    }

    private var statusFooter: some View {
        HStack(spacing: 6) {
            Circle()
                .fill(model.connected ? Color.green : Weft.danger)
                .frame(width: 7, height: 7)
            Text(model.connected ? "localhost:7777" : "daemon offline")
                .font(.weftMono).foregroundStyle(Weft.muted)
            Spacer()
        }
        .padding(.horizontal, 12).padding(.vertical, 8)
    }
}
