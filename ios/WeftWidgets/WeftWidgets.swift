import SwiftUI
import WidgetKit

// PRODUCER-FREE surface: these widgets never touch the engine or the inbox —
// they are static deep links into the app (weft://capture, weft://daily).
// Timelines are pure dates so nothing ever wakes the vault.

// MARK: - Quick Capture (weft://capture)

struct QuickCaptureEntry: TimelineEntry {
    let date: Date
}

struct QuickCaptureProvider: TimelineProvider {
    func placeholder(in context: Context) -> QuickCaptureEntry { .init(date: .now) }

    func getSnapshot(in context: Context, completion: @escaping (QuickCaptureEntry) -> Void) {
        completion(.init(date: .now))
    }

    func getTimeline(in context: Context, completion: @escaping (Timeline<QuickCaptureEntry>) -> Void) {
        // One entry, never refreshed: the widget is a static button.
        completion(Timeline(entries: [.init(date: .now)], policy: .never))
    }
}

struct QuickCaptureView: View {
    @Environment(\.widgetFamily) private var family

    var body: some View {
        Group {
            switch family {
            case .accessoryCircular:
                ZStack {
                    AccessoryWidgetBackground()
                    Image(systemName: "square.and.pencil")
                        .font(.title2)
                }
                .widgetAccentable()
            default: // .systemSmall
                VStack(alignment: .leading, spacing: 6) {
                    Image(systemName: "square.and.pencil")
                        .font(.title)
                        .foregroundStyle(.tint)
                    Spacer(minLength: 0)
                    Text("Quick Capture")
                        .font(.headline)
                    Text("Weft")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .leading)
            }
        }
        .containerBackground(.background, for: .widget)
        .widgetURL(URL(string: "weft://capture"))
    }
}

struct QuickCaptureWidget: Widget {
    var body: some WidgetConfiguration {
        StaticConfiguration(
            kind: "app.tryweft.ios.widgets.capture", provider: QuickCaptureProvider()
        ) { _ in
            QuickCaptureView()
        }
        .configurationDisplayName("Quick Capture")
        .description("Jot a thought straight into today's note.")
        .supportedFamilies([.accessoryCircular, .systemSmall])
    }
}

// MARK: - Today (weft://daily)

struct TodayEntry: TimelineEntry {
    let date: Date
}

struct TodayProvider: TimelineProvider {
    func placeholder(in context: Context) -> TodayEntry { .init(date: .now) }

    func getSnapshot(in context: Context, completion: @escaping (TodayEntry) -> Void) {
        completion(.init(date: .now))
    }

    func getTimeline(in context: Context, completion: @escaping (Timeline<TodayEntry>) -> Void) {
        // Static in the no-engine sense: entries are pure dates, one per
        // upcoming midnight, so the shown date rolls over without waking
        // anything. .atEnd re-extends the window a week at a time.
        let cal = Calendar.current
        let start = cal.startOfDay(for: .now)
        var entries = [TodayEntry(date: .now)]
        for day in 1...7 {
            if let d = cal.date(byAdding: .day, value: day, to: start) {
                entries.append(TodayEntry(date: d))
            }
        }
        completion(Timeline(entries: entries, policy: .atEnd))
    }
}

struct TodayView: View {
    @Environment(\.widgetFamily) private var family
    let date: Date

    var body: some View {
        Group {
            switch family {
            case .accessoryRectangular:
                VStack(alignment: .leading, spacing: 2) {
                    Text(date, format: .dateTime.weekday(.wide))
                        .font(.headline)
                        .widgetAccentable()
                    Text(date, format: .dateTime.month().day())
                        .font(.subheadline)
                    Text("Open today's note")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            default: // .systemSmall
                VStack(alignment: .leading, spacing: 2) {
                    Text(date, format: .dateTime.weekday(.wide))
                        .font(.caption)
                        .textCase(.uppercase)
                        .foregroundStyle(.tint)
                    Text(date, format: .dateTime.day())
                        .font(.system(size: 40, weight: .bold, design: .rounded))
                    Spacer(minLength: 0)
                    Text(date, format: .dateTime.month(.wide).year())
                        .font(.caption)
                    Text("Today in Weft")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .leading)
            }
        }
        .containerBackground(.background, for: .widget)
        .widgetURL(URL(string: "weft://daily"))
    }
}

struct TodayWidget: Widget {
    var body: some WidgetConfiguration {
        StaticConfiguration(
            kind: "app.tryweft.ios.widgets.today", provider: TodayProvider()
        ) { entry in
            TodayView(date: entry.date)
        }
        .configurationDisplayName("Today")
        .description("Open today's daily note.")
        .supportedFamilies([.systemSmall, .accessoryRectangular])
    }
}

// MARK: - Bundle

@main
struct WeftWidgetsBundle: WidgetBundle {
    var body: some Widget {
        QuickCaptureWidget()
        TodayWidget()
    }
}
