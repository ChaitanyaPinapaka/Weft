import SwiftUI
import UIKit
import UniformTypeIdentifiers

/// Principal class of the share extension (NSExtensionPrincipalClass in
/// Info.plist). PRODUCER ONLY — it never links the gomobile engine. It shows
/// the shared text/URL with an optional comment, and Save queues one JSON file
/// in the App-Group inbox; the main app ingests it on next launch/foreground.
final class ShareViewController: UIViewController {
    override func viewDidLoad() {
        super.viewDidLoad()
        view.backgroundColor = .systemBackground
        Task {
            let shared = await loadShared()
            presentSheet(shared)
        }
    }

    // MARK: - Incoming content

    struct SharedContent {
        var text = ""
        var url: String?
        var isEmpty: Bool {
            url == nil && text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        }
    }

    /// Pull the first URL and/or first plain-text attachment off the share
    /// request. Safari hands us a URL; text selections hand us plain text.
    private func loadShared() async -> SharedContent {
        var content = SharedContent()
        let providers = (extensionContext?.inputItems as? [NSExtensionItem] ?? [])
            .flatMap { $0.attachments ?? [] }
        for provider in providers {
            if content.url == nil,
               provider.hasItemConformingToTypeIdentifier(UTType.url.identifier) {
                let item = try? await provider.loadItem(forTypeIdentifier: UTType.url.identifier)
                if let url = item as? URL { content.url = url.absoluteString }
            } else if content.text.isEmpty,
                      provider.hasItemConformingToTypeIdentifier(UTType.plainText.identifier) {
                let item = try? await provider.loadItem(forTypeIdentifier: UTType.plainText.identifier)
                content.text = (item as? String)
                    ?? (item as? Data).flatMap { String(data: $0, encoding: .utf8) }
                    ?? ""
            }
        }
        return content
    }

    // MARK: - UI

    private func presentSheet(_ shared: SharedContent) {
        let root = ShareSheetView(
            shared: shared,
            onSave: { [weak self] comment in self?.save(shared: shared, comment: comment) },
            onCancel: { [weak self] in
                self?.extensionContext?.cancelRequest(withError: CocoaError(.userCancelled))
            })
        let host = UIHostingController(rootView: root)
        addChild(host)
        host.view.frame = view.bounds
        host.view.autoresizingMask = [.flexibleWidth, .flexibleHeight]
        view.addSubview(host.view)
        host.didMove(toParent: self)
    }

    private func save(shared: SharedContent, comment: String) {
        let comment = comment.trimmingCharacters(in: .whitespacesAndNewlines)
        do {
            if let url = shared.url {
                // URL share: text = comment + URL so the entry stands alone;
                // source carries the URL as provenance. (The consumer skips
                // re-appending a source the text already contains.)
                try InboxQueue.enqueue(
                    text: comment.isEmpty ? url : "\(comment)\n\(url)", source: url)
            } else {
                let body = shared.text.trimmingCharacters(in: .whitespacesAndNewlines)
                try InboxQueue.enqueue(
                    text: comment.isEmpty ? body : (body.isEmpty ? comment : "\(comment)\n\(body)"))
            }
            extensionContext?.completeRequest(returningItems: nil)
        } catch {
            let alert = UIAlertController(
                title: "Could not save", message: error.localizedDescription, preferredStyle: .alert)
            alert.addAction(UIAlertAction(title: "OK", style: .default))
            present(alert, animated: true)
        }
    }
}

/// Minimal capture sheet: the shared link/text, an optional comment, Save.
struct ShareSheetView: View {
    let shared: ShareViewController.SharedContent
    let onSave: (String) -> Void
    let onCancel: () -> Void

    @State private var comment = ""

    private var canSave: Bool {
        !shared.isEmpty || !comment.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    var body: some View {
        NavigationStack {
            Form {
                if let url = shared.url {
                    Section("Link") {
                        Text(url)
                            .font(.callout)
                            .foregroundStyle(.secondary)
                            .lineLimit(3)
                    }
                }
                if !shared.text.isEmpty {
                    Section(shared.url == nil ? "Text" : "Selection") {
                        Text(shared.text)
                            .font(.callout)
                            .lineLimit(8)
                    }
                }
                Section {
                    TextField("Add a comment (optional)", text: $comment, axis: .vertical)
                        .lineLimit(1...5)
                }
            }
            .navigationTitle("Capture to Weft")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel", action: onCancel)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") { onSave(comment) }
                        .disabled(!canSave)
                }
            }
        }
    }
}
