import SwiftUI
import Observation
import WeftKit

// "Add a Device": the Mac (responder) side of SAS pairing, over the daemon's
// /api/sync/pairing endpoints. The flow is deliberately human-paced — Approve
// only posts our half of the key exchange; nothing is sealed until the person
// has compared the 8-digit codes and pressed confirm. Closing the sheet
// mid-flow cancels the in-flight approval (best-effort) and stops every poll
// loop, so no timers outlive the sheet.
@MainActor
@Observable
final class PairingModel {
    enum Phase: Equatable {
        case scanning                              // polling the bucket for requests
        case approving(req: String)                // approve posted; waiting for the phone's reveal
        case sas(req: String, code: String)        // codes on both screens; human compares
        case confirming(req: String, code: String) // confirm posted
        case done                                  // sealed — the device is in
        case notConfigured                         // vault has no sync; enroll this Mac first
        case failed(req: String?, message: String) // dead approval / refused approve
        case confirmFailed(req: String, code: String, message: String) // 500 — retryable
    }

    var phase: Phase = .scanning
    var requests: [String] = []
    var hint: String?  // muted transport note under the scanning view

    private let client = WeftClient()
    private var pollTask: Task<Void, Never>?

    // MARK: - Lifecycle

    func start() {
        pollList()
    }

    /// Sheet closed. Stop polling; if an approval is mid-flight, free the
    /// daemon's slot (best-effort — the daemon's cancel is idempotent).
    func stop() {
        pollTask?.cancel()
        pollTask = nil
        if let req = inFlightReq {
            let client = client
            Task { await client.pairingCancel(reqID: req) }
        }
        phase = .scanning
        requests = []
    }

    private var inFlightReq: String? {
        switch phase {
        case .approving(let req), .sas(let req, _), .confirming(let req, _),
             .confirmFailed(let req, _, _):
            return req
        case .failed(let req, _):
            return req
        case .scanning, .done, .notConfigured:
            return nil
        }
    }

    // MARK: - Request-list polling

    private func pollList() {
        pollTask?.cancel()
        pollTask = Task { [weak self] in
            while !Task.isCancelled {
                await self?.fetchListOnce()
                try? await Task.sleep(for: .seconds(2))
            }
        }
    }

    private func fetchListOnce() async {
        do {
            let ids = try await client.pairingRequests()
            // The user may have moved past scanning while this fetch was in
            // the air — don't stomp a later phase.
            guard phase == .scanning || phase == .notConfigured else { return }
            requests = ids
            phase = .scanning
            hint = nil
        } catch let f as WeftClient.PairingFailure where f.status == 409 {
            guard phase == .scanning || phase == .notConfigured else { return }
            requests = []
            phase = .notConfigured
        } catch {
            hint = "Can't reach the daemon — is it running?"
        }
    }

    // MARK: - Approve → SAS

    func approve(_ reqID: String) {
        pollTask?.cancel()
        pollTask = nil
        phase = .approving(req: reqID)
        Task {
            do {
                try await client.pairingApprove(reqID: reqID)
                pollStatus(reqID)
            } catch let f as WeftClient.PairingFailure where f.status == 409 {
                // An earlier approval is parked on the daemon — a previous sheet
                // died without cancelling, or the phone restarted pairing with a
                // fresh id. The 409 carries that parked id; cancel THAT one
                // (cancelling our own new id would be a no-op) and retry once.
                if let parked = f.inFlight, parked != reqID {
                    await client.pairingCancel(reqID: parked)
                    do {
                        try await client.pairingApprove(reqID: reqID)
                        pollStatus(reqID)
                    } catch {
                        phase = .failed(req: nil, message: error.localizedDescription)
                    }
                } else {
                    // No parked id, or it's our own request (e.g. the vault key
                    // is unavailable) — surface the daemon's reason.
                    await client.pairingCancel(reqID: reqID)
                    phase = .failed(req: nil, message: f.message)
                }
            } catch let f as WeftClient.PairingFailure where f.status == 404 {
                phase = .failed(req: nil, message: "That request is gone — start pairing again on the phone.")
            } catch {
                phase = .failed(req: nil, message: error.localizedDescription)
            }
        }
    }

    private func pollStatus(_ reqID: String) {
        pollTask?.cancel()
        pollTask = Task { [weak self] in
            while !Task.isCancelled {
                guard let self, case .approving = self.phase else { return }
                do {
                    switch try await self.client.pairingStatus(reqID: reqID) {
                    case .waiting:
                        break // phone hasn't revealed yet — keep polling
                    case .sas(let code):
                        self.phase = .sas(req: reqID, code: code)
                        return
                    case .error(let msg):
                        // The daemon parks a dead approval (tamper/backend) so
                        // the human SEES the abort; Back fires the cancel that
                        // frees the slot.
                        self.phase = .failed(req: reqID, message: msg)
                        return
                    }
                } catch let f as WeftClient.PairingFailure where f.status == 404 {
                    self.phase = .failed(req: nil, message: "The approval is no longer in flight on the daemon.")
                    return
                } catch {
                    // transient transport error — keep polling
                }
                try? await Task.sleep(for: .seconds(2))
            }
        }
    }

    // MARK: - Confirm / cancel

    /// "Codes match" — the ONLY action that seals the vault key.
    func confirm() {
        guard case .sas(let req, let code) = phase else { return }
        phase = .confirming(req: req, code: code)
        Task {
            do {
                try await client.pairingConfirm(reqID: req)
                phase = .done
            } catch let f as WeftClient.PairingFailure where f.status == 500 {
                // The daemon kept the approval at sas — confirm is retryable.
                phase = .confirmFailed(req: req, code: code, message: f.message)
            } catch let f as WeftClient.PairingFailure {
                phase = .failed(req: req, message: f.message)
            } catch {
                // Transport error: unknown whether the daemon got it. Retry is
                // safe — a 404 (slot already freed) lands in failed.
                phase = .confirmFailed(req: req, code: code, message: error.localizedDescription)
            }
        }
    }

    func retryConfirm() {
        guard case .confirmFailed(let req, let code, _) = phase else { return }
        phase = .sas(req: req, code: code)
        confirm()
    }

    /// Abandon the in-flight approval (without sealing) and rescan.
    func cancelApproval() {
        let req = inFlightReq
        pollTask?.cancel()
        pollTask = nil
        phase = .scanning
        requests = []
        hint = nil
        if let req {
            let client = client
            Task { await client.pairingCancel(reqID: req) }
        }
        pollList()
    }
}

// The "Add a Device…" sheet. Same shell as SetupView: header / divider /
// content, Weft surface tokens throughout.
struct AddDeviceView: View {
    @Environment(\.dismiss) private var dismiss
    @State private var pairing = PairingModel()

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider().overlay(Weft.border)
            content
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 18).padding(.vertical, 16)
        }
        .frame(width: 460)
        .background(Weft.bg)
        .onAppear { pairing.start() }
        .onDisappear { pairing.stop() } // dismissal mid-flow → best-effort cancel
    }

    private var header: some View {
        HStack(spacing: 8) {
            Image(systemName: "iphone.badge.checkmark").foregroundStyle(Weft.accent)
            VStack(alignment: .leading, spacing: 2) {
                Text("Add a Device").font(.system(size: 14, weight: .semibold)).foregroundStyle(Weft.text)
                Text("hand this vault's key to a new device — codes must match on both screens")
                    .font(.weftMono).foregroundStyle(Weft.muted)
            }
            Spacer()
            Button(pairing.phase == .done ? "Done" : "Close") { dismiss() }
                .buttonStyle(.plain).font(.system(size: 12, weight: .medium))
                .foregroundStyle(Weft.accent)
        }
        .padding(.horizontal, 18).padding(.vertical, 14)
    }

    @ViewBuilder private var content: some View {
        switch pairing.phase {
        case .scanning:
            if pairing.requests.isEmpty { instructions } else { requestList }
        case .approving:
            waitingForReveal
        case .sas(_, let code):
            sasView(code: code, busy: false)
        case .confirming(_, let code):
            sasView(code: code, busy: true)
        case .done:
            doneView
        case .notConfigured:
            notConfiguredView
        case .failed(_, let message):
            failedView(message: message)
        case .confirmFailed(_, _, let message):
            confirmFailedView(message: message)
        }
    }

    // MARK: - Scanning

    private var instructions: some View {
        VStack(alignment: .leading, spacing: 10) {
            sectionLabel("WAITING FOR A REQUEST")
            Group {
                Text("1.  On your iPhone, open Weft and enter your bucket credentials.")
                Text("2.  Choose **Add device**, then **Pair with my Mac**.")
                Text("3.  Its pairing request appears here — approve it.")
            }
            .font(.system(size: 12)).foregroundStyle(Weft.text)
            scanningFooter
        }
    }

    private var requestList: some View {
        VStack(alignment: .leading, spacing: 10) {
            sectionLabel("PAIRING REQUESTS")
            ForEach(pairing.requests, id: \.self) { req in
                HStack(spacing: 10) {
                    Image(systemName: "iphone").foregroundStyle(Weft.muted)
                    Text(req).font(.weftMono).foregroundStyle(Weft.text)
                        .textSelection(.enabled)
                    Spacer(minLength: 12)
                    Button("Approve") { pairing.approve(req) }
                        .font(.system(size: 12))
                }
                .padding(.horizontal, 12).padding(.vertical, 10)
                .background(card)
            }
            Text("Approve the request whose code is on the phone's screen.")
                .font(.system(size: 11)).foregroundStyle(Weft.muted)
            scanningFooter
        }
    }

    private var scanningFooter: some View {
        HStack(spacing: 6) {
            ProgressView().controlSize(.small)
            Text(pairing.hint ?? "listening for requests…")
                .font(.weftMono)
                .foregroundStyle(pairing.hint == nil ? Weft.muted : Weft.danger)
        }
        .padding(.top, 4)
    }

    // MARK: - Approving / SAS

    private var waitingForReveal: some View {
        VStack(alignment: .leading, spacing: 10) {
            sectionLabel("APPROVED")
            HStack(spacing: 6) {
                ProgressView().controlSize(.small)
                Text("waiting for the phone to reveal its code…")
                    .font(.weftMono).foregroundStyle(Weft.muted)
            }
            Button("Cancel") { pairing.cancelApproval() }
                .font(.system(size: 12))
        }
    }

    private func sasView(code: String, busy: Bool) -> some View {
        VStack(spacing: 14) {
            sectionLabel("CHECK THE CODE")
            Text(code)
                .font(.system(size: 42, weight: .bold, design: .monospaced))
                .tracking(6)
                .foregroundStyle(Weft.accent)
                .textSelection(.enabled)
                .padding(.horizontal, 18).padding(.vertical, 12)
                .background(card)
            Text("Confirm this matches your iPhone's screen.")
                .font(.system(size: 12)).foregroundStyle(Weft.text)
            HStack(spacing: 10) {
                Button("Cancel") { pairing.cancelApproval() }
                    .font(.system(size: 12))
                if busy {
                    ProgressView().controlSize(.small).padding(.horizontal, 8)
                } else {
                    Button("Codes match — add device") { pairing.confirm() }
                        .font(.system(size: 12, weight: .medium))
                        .buttonStyle(.borderedProminent).tint(Weft.accent)
                        .keyboardShortcut(.defaultAction)
                }
            }
            .disabled(busy)
        }
        .frame(maxWidth: .infinity)
    }

    // MARK: - Terminal states

    private var doneView: some View {
        VStack(spacing: 10) {
            Image(systemName: "checkmark.seal.fill")
                .font(.system(size: 28)).foregroundStyle(.green)
            Text("Device added").font(.system(size: 13, weight: .semibold)).foregroundStyle(Weft.text)
            Text("It pulls the vault on its next sync — nothing else to do here.")
                .font(.system(size: 12)).foregroundStyle(Weft.muted)
            Button("Done") { dismiss() }
                .font(.system(size: 12))
                .keyboardShortcut(.defaultAction)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 6)
    }

    private var notConfiguredView: some View {
        VStack(alignment: .leading, spacing: 10) {
            sectionLabel("SYNC NOT CONFIGURED")
            Text("Pairing hands the vault key to a new device through your sync bucket, so this Mac has to be enrolled first. In a terminal:")
                .font(.system(size: 12)).foregroundStyle(Weft.text)
                .fixedSize(horizontal: false, vertical: true)
            Text("weft sync init   # new bucket\nweft sync join   # existing bucket, with the recovery phrase")
                .font(.weftMono).foregroundStyle(Weft.muted)
                .padding(.horizontal, 12).padding(.vertical, 10)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(card)
                .textSelection(.enabled)
            Text("This panel keeps checking — once sync is set up it moves on by itself.")
                .font(.system(size: 11)).foregroundStyle(Weft.muted)
        }
    }

    private func failedView(message: String) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            sectionLabel("PAIRING STOPPED")
            Text(message)
                .font(.system(size: 12)).foregroundStyle(Weft.danger)
                .fixedSize(horizontal: false, vertical: true)
                .textSelection(.enabled)
            Button("Back") { pairing.cancelApproval() }
                .font(.system(size: 12))
        }
    }

    private func confirmFailedView(message: String) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            sectionLabel("CONFIRM FAILED")
            Text(message)
                .font(.system(size: 12)).foregroundStyle(Weft.danger)
                .fixedSize(horizontal: false, vertical: true)
                .textSelection(.enabled)
            Text("The approval is still live on the daemon — confirming again is safe.")
                .font(.system(size: 11)).foregroundStyle(Weft.muted)
            HStack(spacing: 10) {
                Button("Cancel") { pairing.cancelApproval() }
                    .font(.system(size: 12))
                Button("Try Again") { pairing.retryConfirm() }
                    .font(.system(size: 12, weight: .medium))
                    .keyboardShortcut(.defaultAction)
            }
        }
    }

    // MARK: - Bits

    private func sectionLabel(_ s: String) -> some View {
        Text(s).font(.weftLabel).tracking(0.8).foregroundStyle(Weft.muted)
    }

    private var card: some View {
        RoundedRectangle(cornerRadius: Weft.radius)
            .fill(Weft.surface)
            .overlay(RoundedRectangle(cornerRadius: Weft.radius).strokeBorder(Weft.border))
    }
}
