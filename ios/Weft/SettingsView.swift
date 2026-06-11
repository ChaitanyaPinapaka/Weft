import SwiftUI
import WeftKit

// Bucket credentials + enrollment. Shown as onboarding when the device isn't yet
// enrolled, and as a settings sheet afterward. Two enrollment paths, both backed
// by the existing Go flow: a 24-word recovery phrase, or SAS "add device"
// pairing (the Mac approves with `weft sync pair-approve <reqID>`).
struct SettingsView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss

    @State private var creds = BucketCreds()
    @State private var phrase = ""
    @State private var mode: Mode = .phrase
    @State private var working = false
    @State private var error: String?

    // Pairing progress.
    @State private var reqID: String?
    @State private var sas: String?

    enum Mode: Hashable { case phrase, pair }

    var body: some View {
        NavigationStack {
            Form {
                if model.enrolled {
                    Section {
                        Label("This device is enrolled", systemImage: "checkmark.seal.fill")
                            .foregroundStyle(Weft.accent)
                    }
                }

                Section {
                    Picker("Provider", selection: $creds.provider) {
                        Text("Cloudflare R2").tag("r2")
                        Text("Google Cloud Storage").tag("gcs")
                        Text("AWS S3").tag("aws")
                    }
                    .onChange(of: creds.provider) { _, p in applyPreset(p) }
                    plainField("Endpoint", $creds.endpoint, prompt: endpointHint)
                    plainField("Region", $creds.region, prompt: regionHint)
                    plainField("Bucket", $creds.bucket)
                    plainField("Access Key ID", $creds.accessKeyID)
                    SecureField("Secret Access Key", text: $creds.secret)
                } header: {
                    Text("Bucket")
                } footer: {
                    Text(providerFootnote)
                }

                Section("Enroll this device") {
                    Picker("Method", selection: $mode) {
                        Text("Recovery phrase").tag(Mode.phrase)
                        Text("Add device").tag(Mode.pair)
                    }
                    .pickerStyle(.segmented)

                    if mode == .phrase {
                        TextField("24-word recovery phrase", text: $phrase, axis: .vertical)
                            .lineLimit(3...6)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                        Button("Enroll with phrase", action: enrollPhrase)
                            .disabled(working || phrase.isEmpty)
                    } else {
                        pairingSection
                    }
                }

                if let error {
                    Section { Text(error).foregroundStyle(Weft.danger) }
                }
            }
            .navigationTitle("Weft")
            .toolbar {
                if model.enrolled {
                    ToolbarItem(placement: .topBarTrailing) { Button("Done") { dismiss() } }
                }
            }
        }
    }

    @ViewBuilder private var pairingSection: some View {
        if let reqID {
            LabeledContent("Code for the Mac", value: reqID)
            Text("On your Mac, run:\nweft sync pair-approve \(reqID)")
                .font(.footnote).monospaced().foregroundStyle(Weft.muted)
        }
        if let sas {
            LabeledContent {
                Text(sas).font(.title3.monospaced().bold()).foregroundStyle(Weft.accent)
            } label: {
                Text("Confirm this matches the Mac")
            }
            Button("Codes match — finish", action: finishPair).disabled(working)
        } else if reqID != nil {
            HStack { ProgressView(); Text("waiting for the Mac…").foregroundStyle(Weft.muted) }
        } else {
            Button("Pair with my Mac", action: startPair).disabled(working)
        }
    }

    private func plainField(_ title: String, _ text: Binding<String>, prompt: String? = nil) -> some View {
        TextField(title, text: text, prompt: prompt.map { Text($0) })
            .textInputAutocapitalization(.never)
            .autocorrectionDisabled()
    }

    // MARK: - Provider presets

    /// Sensible defaults per provider, applied on picker change. The region is
    /// real config for AWS (it must come from the user — never a silent "auto")
    /// but is genuinely "auto" for R2 and ignored by the GCS interop endpoint.
    private func applyPreset(_ provider: String) {
        switch provider {
        case "aws":
            creds.region = ""
            creds.endpoint = ""
        case "gcs":
            creds.region = "auto"
            creds.endpoint = "https://storage.googleapis.com"
        default: // r2
            creds.region = "auto"
            creds.endpoint = ""
        }
    }

    private var endpointHint: String {
        switch creds.provider {
        case "aws": return "https://s3.<region>.amazonaws.com"
        case "gcs": return "https://storage.googleapis.com"
        default: return "https://<account-id>.r2.cloudflarestorage.com"
        }
    }

    private var regionHint: String {
        creds.provider == "aws" ? "us-west-2" : "auto"
    }

    private var providerFootnote: String {
        switch creds.provider {
        case "aws":
            return "AWS needs a real region (e.g. us-west-2). Leave the endpoint blank to derive it from the region."
        case "gcs":
            return "Uses the GCS S3-interoperability endpoint with an HMAC key."
        default:
            return "The endpoint is on your R2 dashboard: https://<account-id>.r2.cloudflarestorage.com."
        }
    }

    /// Trimmed, preset-completed credentials — or a clear error before any
    /// network call. For AWS the region must be real, and a blank endpoint is
    /// derived from it; for the rest a blank region falls back to "auto".
    private func preparedCreds() throws -> BucketCreds {
        var c = creds
        c.endpoint = c.endpoint.trimmingCharacters(in: .whitespacesAndNewlines)
        c.region = c.region.trimmingCharacters(in: .whitespacesAndNewlines)
        c.bucket = c.bucket.trimmingCharacters(in: .whitespacesAndNewlines)
        c.accessKeyID = c.accessKeyID.trimmingCharacters(in: .whitespacesAndNewlines)
        if c.provider == "aws" {
            if c.region.isEmpty || c.region == "auto" {
                throw CredsError("AWS S3 needs a real region (e.g. us-west-2) — \"auto\" only works for R2.")
            }
            if c.endpoint.isEmpty {
                c.endpoint = "https://s3.\(c.region).amazonaws.com"
            }
        } else if c.region.isEmpty {
            c.region = "auto"
        }
        return c
    }

    private struct CredsError: LocalizedError {
        let message: String
        init(_ message: String) { self.message = message }
        var errorDescription: String? { message }
    }

    // MARK: - Actions

    private func enrollPhrase() {
        working = true; error = nil
        Task {
            do {
                try await model.enrollWithPhrase(creds: preparedCreds(), phrase: phrase)
                dismissIfPresented()
            } catch { self.error = error.localizedDescription }
            working = false
        }
    }

    private func startPair() {
        working = true; error = nil
        Task {
            do {
                reqID = try await model.pairStart(creds: preparedCreds())
                for _ in 0..<90 where sas == nil {       // ~3 min at 2s
                    try await Task.sleep(for: .seconds(2))
                    let code = try await model.pairPoll()
                    if !code.isEmpty { sas = code }
                }
            } catch { self.error = error.localizedDescription }
            working = false
        }
    }

    private func finishPair() {
        working = true; error = nil
        Task {
            do {
                for _ in 0..<90 {
                    if try await model.pairFinish() { dismissIfPresented(); break }
                    try await Task.sleep(for: .seconds(2))
                }
            } catch { self.error = error.localizedDescription }
            working = false
        }
    }

    private func dismissIfPresented() {
        if model.enrolled { dismiss() } // onboarding flips RootView automatically
    }
}
