import AppKit
import OSLog
import ScreenCaptureKit
import UniformTypeIdentifiers

final class SelectionView: NSView {
    var completed: ((CGRect?) -> Void)?
    private var start: CGPoint?
    private var selection = CGRect.zero
    override var acceptsFirstResponder: Bool { true }

    override func draw(_ dirtyRect: NSRect) {
        let shade = NSBezierPath(rect: bounds)
        shade.appendRect(selection)
        shade.windingRule = .evenOdd
        NSColor.black.withAlphaComponent(0.35).setFill()
        shade.fill()
        NSColor.systemBlue.setStroke()
        NSBezierPath(rect: selection).stroke()
    }

    override func scrollWheel(with event: NSEvent) {
        if start == nil { completed?(nil) }
    }

    override func mouseDown(with event: NSEvent) {
        start = convert(event.locationInWindow, from: nil)
    }

    override func mouseDragged(with event: NSEvent) {
        guard let start else { return }
        let end = convert(event.locationInWindow, from: nil)
        selection = CGRect(x: min(start.x, end.x), y: min(start.y, end.y),
                           width: abs(start.x - end.x), height: abs(start.y - end.y))
            .intersection(bounds).integral
        needsDisplay = true
    }

    override func mouseUp(with event: NSEvent) {
        mouseDragged(with: event)
        guard selection.width >= 100, selection.height >= 100, let window else {
            start = nil
            selection = .zero
            needsDisplay = true
            return
        }
        completed?(window.convertToScreen(convert(selection, to: nil)))
    }

    override func keyDown(with event: NSEvent) {
        if event.keyCode == 53 {
            completed?(nil)
        } else {
            super.keyDown(with: event)
        }
    }
}

final class SelectionWindow: NSPanel {
    override var canBecomeKey: Bool { true }
}

@MainActor
final class App: NSObject, NSApplicationDelegate, NSMenuDelegate {
    private let logger = Logger(subsystem: "local.longshot.mac", category: "capture")
    private let status = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
    private var overlay: SelectionWindow?
    private var outline: NSPanel?
    private var capture: Task<Void, Never>?
    private var stitcher = Stitcher()
    private var finishing = false
    private var partialCapture = false
    private var stopRequested = false
    private var menuOpen = false
    private var message = "打开报告并滚到起点，然后开始框选。"

    func applicationDidFinishLaunching(_ notification: Notification) {
        status.button?.image = Self.menuBarIcon()
        status.button?.imagePosition = .imageLeft
        status.button?.toolTip = "Lshot"
        status.menu = NSMenu()
        status.menu?.delegate = self
    }

    private static func menuBarIcon() -> NSImage {
        let icon = NSImage(size: NSSize(width: 18, height: 18), flipped: false) { _ in
            let path = NSBezierPath()
            path.lineWidth = 1.45
            path.lineCapStyle = .round
            path.lineJoinStyle = .round
            for x: CGFloat in [1.8, 16.2] {
                let dx: CGFloat = x < 9 ? 3.1 : -3.1
                for y: CGFloat in [1.8, 16.2] {
                    let dy: CGFloat = y < 9 ? 3.1 : -3.1
                    path.move(to: NSPoint(x: x + dx, y: y))
                    path.line(to: NSPoint(x: x, y: y))
                    path.line(to: NSPoint(x: x, y: y + dy))
                }
            }
            path.move(to: NSPoint(x: 13.8, y: 6))
            path.appendArc(withCenter: NSPoint(x: 9, y: 6), radius: 4.8, startAngle: 0, endAngle: 180)
            path.move(to: NSPoint(x: 9, y: 6))
            path.line(to: NSPoint(x: 11.3, y: 9))
            NSColor.black.setStroke()
            path.stroke()
            return true
        }
        icon.isTemplate = true
        icon.accessibilityDescription = "Lshot"
        return icon
    }

    func menuNeedsUpdate(_ menu: NSMenu) {
        menu.removeAllItems()
        menu.addItem(withTitle: message, action: nil, keyEquivalent: "")
        menu.addItem(.separator())
        if capture != nil {
            add("结束并保存 PNG…", #selector(stop), to: menu)
            add("取消本次截图", #selector(cancel), to: menu)
        } else if !finishing && overlay == nil {
            add("开始框选…", #selector(beginSelection), to: menu)
            if !CGPreflightScreenCaptureAccess() {
                add("打开屏幕录制权限设置…", #selector(openScreenRecordingSettings), to: menu)
            }
            if stitcher.hasScrolled { add("保存上次截图…", #selector(save), to: menu) }
        }
        menu.addItem(.separator())
        if let version = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String {
            menu.addItem(withTitle: "版本 \(version)", action: nil, keyEquivalent: "")
        }
        add("退出", #selector(quit), to: menu)
    }

    func menuWillOpen(_ menu: NSMenu) { menuOpen = true }
    func menuDidClose(_ menu: NSMenu) { menuOpen = false }

    private func add(_ title: String, _ action: Selector, to menu: NSMenu) {
        menu.addItem(withTitle: title, action: action, keyEquivalent: "").target = self
    }

    @objc private func quit() { NSApp.terminate(nil) }

    @objc private func beginSelection() {
        guard CGPreflightScreenCaptureAccess() || CGRequestScreenCaptureAccess() else {
            message = "请在系统设置中允许「Lshot」录制屏幕，然后重新打开工具。"
            return
        }
        guard let screen = NSScreen.screens.first(where: { $0.frame.contains(NSEvent.mouseLocation) }),
              let primary = NSScreen.screens.first else { return }
        let previousApp = NSWorkspace.shared.frontmostApplication
        let window = SelectionWindow(contentRect: screen.frame, styleMask: [.borderless, .nonactivatingPanel],
                                     backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.hidesOnDeactivate = false
        window.level = .floating
        window.isOpaque = false
        window.backgroundColor = .clear
        window.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary]
        window.setFrame(screen.frame, display: false)
        let view = SelectionView(frame: CGRect(origin: .zero, size: screen.frame.size))
        view.completed = { [weak self] rect in
            guard let self else { return }
            self.overlay?.orderOut(nil)
            self.overlay = nil
            if NSApp.isActive {
                if let previousApp, previousApp.processIdentifier != ProcessInfo.processInfo.processIdentifier {
                    previousApp.activate()
                } else {
                    NSApp.deactivate()
                }
            }
            guard let rect else { return }
            self.showOutline(around: rect)
            let origin = CGPoint(x: rect.minX, y: primary.frame.maxY - rect.maxY)
            self.start(CGRect(origin: origin, size: rect.size))
        }
        window.contentView = view
        overlay = window
        window.makeKeyAndOrderFront(nil)
        window.makeFirstResponder(view)
    }

    @objc private func openScreenRecordingSettings() {
        if let url = URL(string: "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture") {
            NSWorkspace.shared.open(url)
        }
    }

    private func showOutline(around rect: CGRect) {
        let panel = NSPanel(contentRect: rect.insetBy(dx: -4, dy: -4),
                            styleMask: [.borderless, .nonactivatingPanel], backing: .buffered, defer: false)
        panel.isReleasedWhenClosed = false
        panel.hidesOnDeactivate = false
        panel.ignoresMouseEvents = true
        panel.level = .floating
        panel.isOpaque = false
        panel.hasShadow = false
        panel.backgroundColor = .clear
        panel.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary]
        panel.setFrame(rect.insetBy(dx: -4, dy: -4), display: false)
        panel.contentView?.wantsLayer = true
        panel.contentView?.layer?.borderColor = NSColor.systemBlue.cgColor
        panel.contentView?.layer?.borderWidth = 2
        outline = panel
        panel.orderFrontRegardless()
    }

    private func resetCaptureUI() {
        outline?.orderOut(nil)
        outline = nil
        capture = nil
        status.button?.title = ""
        message = "打开报告并滚到起点，然后开始框选。"
    }

    private func start(_ rect: CGRect) {
        stitcher = Stitcher()
        partialCapture = false
        stopRequested = false
        status.button?.title = "准备中"
        message = "正在读取第一帧，请稍等。"
        capture = Task {
            var mismatches = 0
            var frames = 0
            var accepted = 0
            var rejected = 0
            defer {
                logger.info("Capture ended: \(frames) frames, \(accepted) appended, \(rejected) unmatched")
            }
            var reachedLimit = false
            do {
                try await Task.sleep(for: .milliseconds(120))
                while !stopRequested {
                    if menuOpen {
                        try await Task.sleep(for: .milliseconds(100))
                        continue
                    }
                    let image = try await SCScreenshotManager.captureImage(in: rect)
                    try Task.checkCancellation()
                    if stopRequested { break }
                    if menuOpen { continue }
                    frames += 1
                    let result = stitcher.append(image)
                    switch result {
                    case .added, .unchanged:
                        if result == .added { accepted += 1 }
                        mismatches = 0
                        if stitcher.hasScrolled {
                            message = "已拼接 \(stitcher.height) 像素"
                            status.button?.title = "\(stitcher.height) px"
                        } else {
                            message = "请在蓝框内向下滚动。"
                            status.button?.title = "等待滚动"
                        }
                    case .mismatch:
                        rejected += 1
                        mismatches += 1
                        if mismatches >= 3 {
                            message = "画面未对齐。请慢慢滚回上一段，再向下滚。"
                            status.button?.title = "未对齐"
                        }
                    case .full:
                        reachedLimit = true
                        stopRequested = true
                    }
                    if !stopRequested { try await Task.sleep(for: .milliseconds(35)) }
                }
                try Task.checkCancellation()
                resetCaptureUI()
                guard stitcher.hasScrolled else {
                    alert("没有生成长截图", "本次没有拼接到第二屏，未保存文件。请滚回起点，重新框选后分几次向下滚动。")
                    return
                }
                partialCapture = reachedLimit || mismatches > 0
                if reachedLimit {
                    alert("已达到单张图片大小上限", "可以保存已经拼接的部分，再分段截图。")
                } else if partialCapture {
                    alert("截图未完整", "末尾画面没有接上。可以保存已拼接的部分，文件名会标明「未完整」。")
                }
                save()
            } catch {
                resetCaptureUI()
                if !Task.isCancelled {
                    alert("无法继续截图", error.localizedDescription)
                }
            }
        }
    }

    @objc private func stop() {
        stopRequested = true
        message = "正在结束截图…"
    }

    @objc private func cancel() {
        stopRequested = true
        capture?.cancel()
        stitcher = Stitcher()
    }

    @objc private func save() {
        guard stitcher.hasScrolled, !finishing else { return }
        finishing = true
        let panel = NSSavePanel()
        panel.allowedContentTypes = [.png]
        panel.nameFieldStringValue = partialCapture ? "LShot-未完整.png" : "长截图.png"
        NSApp.activate()
        guard panel.runModal() == .OK, let url = panel.url else {
            finishing = false
            return
        }
        let capture = stitcher
        Task {
            defer { finishing = false }
            do {
                try await Task.detached(priority: .userInitiated) {
                    guard let image = capture.combinedImage() else { throw CocoaError(.fileWriteUnknown) }
                    try PNGExport.encode(image).write(to: url, options: .atomic)
                }.value
            } catch {
                alert("保存失败", error.localizedDescription)
            }
        }
    }

    private func alert(_ title: String, _ text: String) {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = text
        NSApp.activate()
        alert.runModal()
    }
}

MainActor.assumeIsolated {
    let app = NSApplication.shared
    let delegate = App()
    app.setActivationPolicy(.accessory)
    app.delegate = delegate
    withExtendedLifetime(delegate) { app.run() }
}
