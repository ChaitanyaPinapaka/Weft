import Carbon.HIToolbox
import AppKit

// A single system-wide hotkey (⌃⌥Space) to summon quick capture from any app.
// Carbon's RegisterEventHotKey is the no-special-permission way to do this;
// NSEvent global monitors would need Accessibility access. The C event handler
// carries `self` through userData and bounces the trigger to the main queue.
final class HotkeyManager {
    var onTrigger: () -> Void = {}

    private var hotKeyRef: EventHotKeyRef?
    private var handlerRef: EventHandlerRef?

    func register() {
        let hotKeyID = EventHotKeyID(signature: "WEFT".fourCharCode, id: 1)
        let mods = UInt32(controlKey | optionKey)
        RegisterEventHotKey(UInt32(kVK_Space), mods, hotKeyID,
                            GetApplicationEventTarget(), 0, &hotKeyRef)

        var spec = EventTypeSpec(eventClass: OSType(kEventClassKeyboard),
                                 eventKind: UInt32(kEventHotKeyPressed))
        InstallEventHandler(GetApplicationEventTarget(), { _, _, userData in
            guard let userData else { return noErr }
            let manager = Unmanaged<HotkeyManager>.fromOpaque(userData).takeUnretainedValue()
            DispatchQueue.main.async { manager.onTrigger() }
            return noErr
        }, 1, &spec, Unmanaged.passUnretained(self).toOpaque(), &handlerRef)
    }

    deinit {
        if let hotKeyRef { UnregisterEventHotKey(hotKeyRef) }
        if let handlerRef { RemoveEventHandler(handlerRef) }
    }
}

private extension String {
    var fourCharCode: FourCharCode {
        var result: FourCharCode = 0
        for byte in utf8.prefix(4) { result = (result << 8) + FourCharCode(byte) }
        return result
    }
}
