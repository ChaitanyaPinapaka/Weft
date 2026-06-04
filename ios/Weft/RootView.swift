import SwiftUI
import WeftKit

// Top-level surface: a boot spinner, then either onboarding (SettingsView) until
// the device is enrolled, or the main vault UI.
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
        .tint(Weft.accent)
    }
}

// The enrolled experience: a foldered notes list that pushes into the reader, an
// inline capture bar, sync, and settings.
struct MainView: View {
    @Environment(AppModel.self) private var model
    @State private var path: [String] = []
    @State private var showSettings = false
    @State private var captureText = ""

    var body: some View {
        NavigationStack(path: $path) {
            notesList
                .navigationTitle("Weft")
                .navigationDestination(for: String.self) { p in
                    ReaderScreen(path: p) { linked in path.append(linked) }
                }
                .toolbar {
                    ToolbarItem(placement: .topBarLeading) {
                        Button {
                            Task { await model.openDaily(); if let p = model.currentPath { path.append(p) } }
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
        .task { if model.notes.isEmpty { await model.reload() } }
    }

    private var notesList: some View {
        List {
            ForEach(model.groupedNotes, id: \.folder) { group in
                Section(group.folder.isEmpty ? "vault" : group.folder) {
                    ForEach(group.notes) { note in
                        NavigationLink(value: note.path) {
                            Text(note.title).foregroundStyle(Weft.text).lineLimit(1)
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

    private var captureBar: some View {
        HStack(spacing: 8) {
            TextField("capture a thought…", text: $captureText)
                .textFieldStyle(.roundedBorder)
                .onSubmit(submit)
            if model.syncing { ProgressView() }
        }
    }

    private func submit() {
        let t = captureText
        captureText = ""
        Task { await model.capture(t) }
    }
}

// One pushed reader. Opens the note on appear; renders the shared reader once the
// model's current doc matches this path. In-note links push further screens.
struct ReaderScreen: View {
    @Environment(AppModel.self) private var model
    let path: String
    let onLink: (String) -> Void

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
        .task(id: path) { await model.open(path: path) }
    }
}
