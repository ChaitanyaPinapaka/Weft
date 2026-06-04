import SwiftUI
import WeftKit

// The brain panel: what Weft would surface for the open note, rendered natively
// from GET /api/surface. Mirrors web/viewer.js — thought trail, backlinks,
// activation-ranked "surfaced" (filtered to Spread>0, temperature by opacity,
// resurfaced marked with ✦), and on-this-day. Restraint over decoration.
struct BrainPanel: View {
    @Environment(AppModel.self) private var model

    private var scored: [Scored] {
        Array((model.surface.scored ?? []).filter { $0.spread > 0 }.prefix(12))
    }
    private var backlinks: [String] { model.surface.backlinks ?? [] }
    private var onThisDay: [Candidate] { model.surface.onThisDay ?? [] }
    private var trail: [String] { model.surface.trail ?? [] }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 26) {
                if trail.count > 1 { trailSection }
                backlinksSection
                if !scored.isEmpty { surfacedSection }
                if !onThisDay.isEmpty { onThisDaySection }
            }
            .padding(20)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .background(Weft.bg)
    }

    // MARK: - Sections

    private var trailSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            sectionHeader("Trail")
            // focus first; show the earlier-session path as faint crumbs.
            FlowText(crumbs: trail.reversed().map { ($0, model.titleFor($0)) }) { path in
                model.open(path: path)
            }
        }
    }

    private var backlinksSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            sectionHeader("Backlinks")
            if backlinks.isEmpty {
                Text("no backlinks yet")
                    .font(.system(size: 12)).foregroundStyle(Weft.muted)
            } else {
                ForEach(backlinks, id: \.self) { path in
                    LinkRow(title: model.titleFor(path)) { model.open(path: path) }
                }
            }
        }
    }

    private var surfacedSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            sectionHeader("Surfaced")
            let denom = Double(max(1, scored.count - 1))
            ForEach(Array(scored.enumerated()), id: \.element.id) { idx, item in
                SurfaceCard(item: item, opacity: temperature(Double(idx) / denom)) {
                    model.open(path: item.path)
                }
            }
        }
    }

    private var onThisDaySection: some View {
        VStack(alignment: .leading, spacing: 8) {
            sectionHeader("On this day")
            ForEach(onThisDay) { c in
                Button { model.open(path: c.path) } label: {
                    VStack(alignment: .leading, spacing: 2) {
                        Text(c.displayTitle).font(.system(size: 13, weight: .medium))
                            .foregroundStyle(Weft.text).lineLimit(1)
                        Text("\(c.yearsAgo()) year\(c.yearsAgo() == 1 ? "" : "s") ago")
                            .font(.system(size: 11)).foregroundStyle(Weft.muted)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
                .buttonStyle(.plain)
            }
        }
    }

    private func sectionHeader(_ text: String) -> some View {
        Text(text.uppercased())
            .font(.weftLabel).tracking(0.8)
            .foregroundStyle(Weft.muted)
    }

    // top item full, dimming toward a 0.55 floor — opacity-as-temperature.
    private func temperature(_ t: Double) -> Double { max(0.55, 1.0 - 0.45 * t) }
}

// A surfaced note: title, reason chips, resurfaced accent. The whole card is a
// borderless button that fills on hover.
private struct SurfaceCard: View {
    let item: Scored
    let opacity: Double
    let action: () -> Void
    @State private var hovering = false

    var body: some View {
        Button(action: action) {
            VStack(alignment: .leading, spacing: 5) {
                HStack(spacing: 5) {
                    if item.isResurfaced {
                        Text("✦").font(.system(size: 11)).foregroundStyle(Weft.accent)
                    }
                    Text(item.displayTitle)
                        .font(.system(size: 13, weight: .medium))
                        .foregroundStyle(Weft.text).lineLimit(1)
                }
                if let chips = item.reasons?.filter({ $0 != "base" }), !chips.isEmpty {
                    HStack(spacing: 4) {
                        ForEach(chips, id: \.self) { Chip(text: reasonLabel($0)) }
                    }
                }
            }
            .padding(.vertical, 7).padding(.horizontal, 9)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(
                RoundedRectangle(cornerRadius: Weft.radius)
                    .fill(hovering ? Weft.surface : .clear)
                    .overlay(RoundedRectangle(cornerRadius: Weft.radius)
                        .strokeBorder(hovering ? Weft.border : .clear))
            )
            .overlay(alignment: .leading) {
                if item.isResurfaced {
                    Rectangle().fill(Weft.accent.opacity(0.45)).frame(width: 2)
                }
            }
        }
        .buttonStyle(.plain)
        .opacity(opacity)
        .onHover { hovering = $0 }
    }
}

private struct Chip: View {
    let text: String
    var body: some View {
        Text(text)
            .font(.weftMono)
            .foregroundStyle(Weft.muted)
            .padding(.horizontal, 6).padding(.vertical, 1)
            .background(Capsule().fill(Weft.border))
    }
}

private struct LinkRow: View {
    let title: String
    let action: () -> Void
    @State private var hovering = false
    var body: some View {
        Button(action: action) {
            Text(title)
                .font(.system(size: 13))
                .foregroundStyle(hovering ? Weft.accent : Weft.text)
                .lineLimit(1)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .buttonStyle(.plain)
        .onHover { hovering = $0 }
    }
}

// The thought-trail breadcrumb: faint crumbs separated by ›, wrapping naturally.
private struct FlowText: View {
    let crumbs: [(path: String, title: String)]
    let onTap: (String) -> Void

    var body: some View {
        Text(attributed)
            .font(.system(size: 11))
            .foregroundStyle(Weft.muted)
            .lineLimit(3)
    }

    private var attributed: AttributedString {
        var out = AttributedString()
        for (i, c) in crumbs.enumerated() {
            if i > 0 {
                var sep = AttributedString(" › ")
                sep.foregroundColor = Weft.border
                out += sep
            }
            out += AttributedString(c.title)
        }
        return out
    }
}
