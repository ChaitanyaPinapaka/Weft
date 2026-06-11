import SwiftUI
import WeftKit

// Top-level surface: a boot spinner, then either onboarding (SettingsView) until
// the device is enrolled, or the main vault UI. The error banner overlays here —
// above onboarding too — so boot/enroll failures are never silent.
struct RootView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Group {
            if model.booting {
                ProgressView()
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                    .background(Weft.bg.ignoresSafeArea())
            } else if !model.enrolled {
                SettingsView() // onboarding — flips to MainView on enroll
            } else {
                MainView()
            }
        }
        .overlay(alignment: .top) {
            if let message = model.lastError {
                ErrorBanner(message: message) { model.lastError = nil }
            }
        }
        .animation(.snappy, value: model.lastError)
        .tint(Weft.accent)
    }
}

// A dismissible error strip. AppModel.lastError collects every background
// failure (sync, open, capture); this is the one place they surface.
struct ErrorBanner: View {
    let message: String
    let dismiss: () -> Void

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Image(systemName: "exclamationmark.triangle.fill").font(.footnote)
            Text(message).font(.footnote).lineLimit(3)
            Spacer(minLength: 0)
            Button(action: dismiss) {
                Image(systemName: "xmark").font(.footnote.bold())
            }
            .accessibilityLabel("Dismiss error")
        }
        .foregroundStyle(Weft.danger)
        .padding(10)
        .background(Weft.surface, in: RoundedRectangle(cornerRadius: Weft.radius))
        .overlay(RoundedRectangle(cornerRadius: Weft.radius).stroke(Weft.border))
        .padding(.horizontal, 12)
        .transition(.move(edge: .top).combined(with: .opacity))
    }
}

// The enrolled experience: a foldered notes list that pushes into the reader,
// search, an inline capture bar + full capture sheet, sync, and settings.
struct MainView: View {
    @Environment(AppModel.self) private var model
    @State private var showSettings = false
    @State private var captureText = ""
    @State private var query = ""
    @State private var hits: [SearchHit] = []
    @State private var confirmTrash = false
    @State private var trashCandidate: NoteRef?

    private var trimmedQuery: String { query.trimmingCharacters(in: .whitespaces) }

    var body: some View {
        @Bindable var model = model
        NavigationStack(path: $model.navPath) {
            notesList
                .navigationTitle("Weft")
                .navigationDestination(for: String.self) { p in
                    ReaderScreen(path: p) { linked in model.navPath.append(linked) }
                }
                .searchable(text: $query, prompt: "Search notes")
                .task(id: query) {
                    guard !trimmedQuery.isEmpty else { hits = []; return }
                    try? await Task.sleep(for: .milliseconds(150)) // debounce keystrokes
                    guard !Task.isCancelled else { return }
                    hits = await model.search(trimmedQuery)
                }
                .safeAreaInset(edge: .top, spacing: 0) { syncStatus }
                .toolbar {
                    ToolbarItem(placement: .topBarLeading) {
                        Button {
                            Task { await model.openDaily(); if let p = model.currentPath { model.navPath.append(p) } }
                        } label: { Label("Today", systemImage: "sun.max") }
                    }
                    ToolbarItem(placement: .topBarTrailing) {
                        Button { Task { await model.sync() } } label: { Image(systemName: "arrow.clockwise") }
                            .disabled(model.syncing)
                    }
                    ToolbarItem(placement: .topBarTrailing) {
                        Button { showSettings = true } label: { Image(systemName: "gearshape") }
                    }
                    ToolbarItem(placement: .bottomBar) { captureBar }
                }
        }
        .sheet(isPresented: $showSettings) { SettingsView() }
        .sheet(isPresented: $model.showCapture) { CaptureSheet() }
        .confirmationDialog(
            "Remove note?", isPresented: $confirmTrash, titleVisibility: .visible,
            presenting: trashCandidate
        ) { note in
            Button("Move to .trash/", role: .destructive) {
                Task { await model.trash(path: note.path) }
            }
            Button("Cancel", role: .cancel) {}
        } message: { note in
            // Mirrors the web viewer's wording: soft delete, never destruction.
            Text("\"\(note.title)\" moves to .trash/ in your vault — nothing is deleted — but it leaves listings, search, and surfacing.")
        }
        .task { if model.notes.isEmpty { await model.reload() } }
    }

    private var notesList: some View {
        List {
            if trimmedQuery.isEmpty {
                ForEach(model.groupedNotes, id: \.folder) { group in
                    Section(group.folder.isEmpty ? "vault" : group.folder) {
                        ForEach(group.notes) { note in
                            NavigationLink(value: note.path) {
                                Text(note.title).foregroundStyle(Weft.text).lineLimit(1)
                            }
                            .swipeActions(edge: .trailing) {
                                Button(role: .destructive) {
                                    trashCandidate = note
                                    confirmTrash = true
                                } label: { Label("Trash", systemImage: "trash") }
                            }
                        }
                    }
                }
            } else if hits.isEmpty {
                Text("No matches").foregroundStyle(Weft.muted)
            } else {
                ForEach(hits) { hit in
                    NavigationLink(value: hit.path) {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(hit.displayTitle).foregroundStyle(Weft.text).lineLimit(1)
                            Text(hit.attributedSnippet)
                                .font(.footnote).foregroundStyle(Weft.muted).lineLimit(2)
                        }
                    }
                }
            }
        }
        .listStyle(.insetGrouped)
        .refreshable { await model.sync() }
        .overlay {
            if model.notes.isEmpty {
                ContentUnavailableView("Empty vault", systemImage: "tray",
                                       description: Text("Capture a thought, or sync from your bucket."))
            }
        }
    }

    /// One quiet line under the nav title: in-flight state, or when + how much
    /// the last sync moved (counts from the engine's Result JSON).
    @ViewBuilder private var syncStatus: some View {
        if model.syncing || model.lastSyncAt != nil {
            HStack(spacing: 4) {
                if model.syncing {
                    ProgressView().controlSize(.mini)
                    Text("syncing…")
                } else if let at = model.lastSyncAt {
                    Text(syncSummary(at))
                }
            }
            .font(.weftMono)
            .foregroundStyle(Weft.muted)
            .frame(maxWidth: .infinity)
            .padding(.vertical, 3)
            .background(.bar)
        }
    }

    private func syncSummary(_ at: Date) -> String {
        var s = "synced \(at.formatted(date: .omitted, time: .shortened))"
        if let r = model.lastSyncResult {
            s += " · \(r.pushed) pushed · \(r.applied) applied"
        }
        return s
    }

    private var captureBar: some View {
        HStack(spacing: 8) {
            TextField("capture a thought…", text: $captureText)
                .textFieldStyle(.roundedBorder)
                .onSubmit(submit)
            Button {
                // Expand: carry whatever's typed into the full sheet.
                model.captureDraft = captureText
                captureText = ""
                model.showCapture = true
            } label: { Image(systemName: "arrow.up.left.and.arrow.down.right") }
                .accessibilityLabel("Open capture sheet")
        }
    }

    private func submit() {
        let t = captureText
        captureText = ""
        Task { await model.capture(t) }
    }
}

// The full capture sheet: a multiline editor that appends to today's daily note.
// Also the landing spot for weft://capture?text=… (AppModel prefills the draft).
struct CaptureSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @FocusState private var focused: Bool

    var body: some View {
        @Bindable var model = model
        NavigationStack {
            TextEditor(text: $model.captureDraft)
                .focused($focused)
                .scrollContentBackground(.hidden)
                .padding(8)
                .background(Weft.bg.ignoresSafeArea())
                .navigationTitle("Capture")
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Cancel") {
                            model.captureDraft = ""
                            dismiss()
                        }
                    }
                    ToolbarItem(placement: .confirmationAction) {
                        Button("Save") {
                            let t = model.captureDraft
                            model.captureDraft = ""
                            dismiss()
                            Task { await model.capture(t) }
                        }
                        .disabled(model.captureDraft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                    }
                }
        }
        .presentationDetents([.medium, .large])
        .onAppear { focused = true }
    }
}

// One pushed reader. Opens the note on appear; renders the shared reader once the
// model's current doc matches this path. In-note links push further screens.
struct ReaderScreen: View {
    @Environment(AppModel.self) private var model
    let path: String
    let onLink: (String) -> Void
    @State private var showBrain = false

    var body: some View {
        ZStack {
            Weft.bg.ignoresSafeArea()
            if let doc = model.doc, doc.path == path {
                ReaderView(doc: doc, onNavigate: onLink)
                    .ignoresSafeArea(edges: .bottom)
            } else {
                ProgressView()
            }
        }
        .navigationTitle(model.titleFor(path))
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button { showBrain.toggle() } label: { Image(systemName: "brain") }
                    .accessibilityLabel("What Weft surfaced")
            }
        }
        // Adaptive: a trailing column on iPad, a sheet on iPhone. Opening a
        // surfaced note dismisses the panel, then pushes it onto the stack.
        .inspector(isPresented: $showBrain) {
            BrainPanel(
                surface: model.surface,
                titleFor: { model.titleFor($0) },
                onOpen: { p in showBrain = false; onLink(p) }
            )
            .inspectorColumnWidth(min: 260, ideal: 300, max: 380)
        }
        .task(id: path) { await model.open(path: path) }
    }
}
