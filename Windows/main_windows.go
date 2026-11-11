package main

import (
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"unsafe"
)

var (
	user   = syscall.NewLazyDLL("user32.dll")
	gdi    = syscall.NewLazyDLL("gdi32.dll")
	kernel = syscall.NewLazyDLL("kernel32.dll")
	dialog = syscall.NewLazyDLL("comdlg32.dll")
	dwm    = syscall.NewLazyDLL("dwmapi.dll")
)

//go:uintptrescapes
func call(dll *syscall.LazyDLL, name string, args ...uintptr) uintptr {
	v, _, _ := dll.NewProc(name).Call(args...)
	return v
}

func text(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

type point struct{ x, y int32 }
type rect struct{ left, top, right, bottom int32 }

func (r rect) image() image.Rectangle {
	return image.Rect(int(r.left), int(r.top), int(r.right), int(r.bottom))
}

type wndClass struct {
	size, style                        uint32
	proc                               uintptr
	classExtra, windowExtra            int32
	instance, icon, cursor, background uintptr
	menuName, name                     *uint16
	smallIcon                          uintptr
}

type msg struct {
	window  uintptr
	id      uint32
	w, l    uintptr
	time    uint32
	pt      point
	private uint32
}

type paint struct {
	dc              uintptr
	erase           int32
	box             rect
	restore, update int32
	reserved        [32]byte
}

type bitmapHeader struct {
	size                        uint32
	width, height               int32
	planes, bits                uint16
	compression, imageSize      uint32
	xResolution, yResolution    int32
	colorsUsed, colorsImportant uint32
}

type monitorInfo struct {
	size          uint32
	monitor, work rect
	flags         uint32
}

type saveDialog struct {
	size                         uint32
	owner, instance              uintptr
	filter, customFilter         *uint16
	maxCustomFilter, filterIndex uint32
	file                         *uint16
	maxFile                      uint32
	fileTitle                    *uint16
	maxFileTitle                 uint32
	initialDir, title            *uint16
	flags                        uint32
	fileOffset, fileExtension    uint16
	defaultExtension             *uint16
	customData, hook             uintptr
	templateName                 *uint16
	reserved                     uintptr
	reservedInt, flagsEx         uint32
}

const (
	wmDestroy        = 0x0002
	wmPaint          = 0x000f
	wmClose          = 0x0010
	wmCancelMode     = 0x001f
	wmKeyDown        = 0x0100
	wmCommand        = 0x0111
	wmTimer          = 0x0113
	wmMouseMove      = 0x0200
	wmLeftDown       = 0x0201
	wmLeftUp         = 0x0202
	wmRightDown      = 0x0204
	wmCaptureChanged = 0x0215
	wmHotKey         = 0x0312

	startID   = 101
	stopID    = 102
	cancelID  = 103
	timerID   = 1
	saveKey   = 201
	cancelKey = 202
)

type application struct {
	instance, window, overlay, label, startButton, stopButton, cancelButton uintptr
	desktop, selection                                                      image.Rectangle
	anchor                                                                  image.Point
	selecting, dragging, capturing                                          bool
	misses                                                                  int
	saveHotkey, cancelHotkey                                                bool
	stitch                                                                  stitcher
	grabber                                                                 *screenGrabber
}

var app application

func main() {
	runtime.LockOSThread()
	if proc := user.NewProc("SetProcessDpiAwarenessContext"); proc.Find() == nil {
		proc.Call(^uintptr(3)) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2
	}
	app.instance = call(kernel, "GetModuleHandleW", 0)
	if !app.createWindows() {
		message(0, "无法启动 LShot。", 0x10)
		return
	}
	call(user, "ShowWindow", app.window, 1)
	call(user, "UpdateWindow", app.window)
	var event msg
	for {
		result := int32(call(user, "GetMessageW", uintptr(unsafe.Pointer(&event)), 0, 0, 0))
		if result <= 0 {
			break
		}
		call(user, "TranslateMessage", uintptr(unsafe.Pointer(&event)))
		call(user, "DispatchMessageW", uintptr(unsafe.Pointer(&event)))
	}
}

func (a *application) createWindows() bool {
	register := func(name string, proc uintptr, cursor uintptr) bool {
		class := wndClass{
			proc: proc, instance: a.instance, cursor: call(user, "LoadCursorW", 0, cursor),
			background: 6, icon: call(user, "LoadIconW", 0, 32512),
		}
		class.name, _ = syscall.UTF16PtrFromString(name)
		class.size = uint32(unsafe.Sizeof(class))
		return call(user, "RegisterClassExW", uintptr(unsafe.Pointer(&class))) != 0
	}
	if !register("LShotWindow", syscall.NewCallback(windowProc), 32512) ||
		!register("LShotSelection", syscall.NewCallback(overlayProc), 32515) {
		return false
	}

	a.window = call(user, "CreateWindowExW", 8, uintptr(unsafe.Pointer(text("LShotWindow"))), uintptr(unsafe.Pointer(text("LShot 长截图"))),
		0x00ca0000, 80, 80, 526, 170, 0, 0, a.instance, 0)
	if a.window == 0 {
		return false
	}
	dpi := call(user, "GetDpiForWindow", a.window)
	if dpi == 0 {
		dpi = 96
	}
	scale := func(value uintptr) uintptr { return value * dpi / 96 }
	call(user, "SetWindowPos", a.window, 0, 0, 0, scale(526), scale(170), 0x16)
	child := func(class, title string, id, x, y, w, h uintptr) uintptr {
		control := call(user, "CreateWindowExW", 0, uintptr(unsafe.Pointer(text(class))), uintptr(unsafe.Pointer(text(title))), 0x50000000,
			scale(x), scale(y), scale(w), scale(h), a.window, id, a.instance, 0)
		font := call(gdi, "GetStockObject", 17)
		call(user, "SendMessageW", control, 0x30, font, 1)
		return control
	}
	a.label = child("STATIC", "打开报告，点「开始框选」并拖出截图区域。", 0, 16, 15, 476, 40)
	a.startButton = child("BUTTON", "开始框选", startID, 16, 65, 145, 32)
	a.stopButton = child("BUTTON", "结束并保存 PNG", stopID, 173, 65, 154, 32)
	a.cancelButton = child("BUTTON", "取消本次", cancelID, 339, 65, 145, 32)
	if a.label == 0 || a.startButton == 0 || a.stopButton == 0 || a.cancelButton == 0 {
		return false
	}
	a.controls(false)
	return true
}

func (a *application) status(s string) {
	call(user, "SetWindowTextW", a.label, uintptr(unsafe.Pointer(text(s))))
}

func (a *application) controls(active bool) {
	if active {
		call(user, "EnableWindow", a.startButton, 0)
		call(user, "EnableWindow", a.stopButton, 1)
		call(user, "EnableWindow", a.cancelButton, 1)
	} else {
		call(user, "EnableWindow", a.startButton, 1)
		call(user, "EnableWindow", a.stopButton, 0)
		call(user, "EnableWindow", a.cancelButton, 0)
	}
}

func windowProc(hwnd uintptr, id uint32, w, l uintptr) uintptr {
	switch id {
	case wmCommand:
		switch w & 0xffff {
		case startID:
			app.beginSelection()
		case stopID:
			app.finish(true)
		case cancelID:
			app.finish(false)
		}
		return 0
	case wmTimer:
		if w == timerID && app.capturing {
			app.capture()
		}
		return 0
	case wmHotKey:
		switch w {
		case saveKey:
			app.finish(true)
		case cancelKey:
			app.finish(false)
		}
		return 0
	case wmClose:
		if app.capturing {
			app.finish(false)
		}
		call(user, "DestroyWindow", hwnd)
		return 0
	case wmDestroy:
		call(user, "PostQuitMessage", 0)
		return 0
	}
	return call(user, "DefWindowProcW", hwnd, uintptr(id), w, l)
}

func (a *application) beginSelection() {
	if a.selecting || a.capturing {
		return
	}
	metric := func(id uintptr) int { return int(int32(call(user, "GetSystemMetrics", id))) }
	x, y := metric(76), metric(77)
	w, h := metric(78), metric(79)
	if w <= 0 || h <= 0 {
		return
	}
	a.desktop = image.Rect(x, y, x+w, y+h)
	a.selection = image.Rectangle{}
	a.selecting = true
	call(user, "ShowWindow", a.window, 0)
	a.overlay = call(user, "CreateWindowExW", 0x80088, uintptr(unsafe.Pointer(text("LShotSelection"))), 0,
		0x80000000, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 0, 0, a.instance, 0)
	if a.overlay == 0 {
		a.closeSelection(false)
		message(a.window, "无法打开框选界面。", 0x10)
		return
	}
	call(user, "SetLayeredWindowAttributes", a.overlay, 0, 130, 2)
	call(user, "ShowWindow", a.overlay, 5)
	call(user, "SetForegroundWindow", a.overlay)
	call(user, "SetFocus", a.overlay)
}

func overlayProc(hwnd uintptr, id uint32, w, l uintptr) uintptr {
	switch id {
	case wmLeftDown:
		app.anchor = cursor()
		app.dragging = true
		call(user, "SetCapture", hwnd)
		return 0
	case wmMouseMove, wmLeftUp:
		if app.dragging {
			p := cursor()
			app.selection = image.Rect(app.anchor.X, app.anchor.Y, p.X, p.Y).Intersect(app.desktop)
			if id == wmLeftUp {
				app.dragging = false
				call(user, "ReleaseCapture")
				app.closeSelection(app.selection.Dx() >= 100 && app.selection.Dy() >= 100)
			} else {
				call(user, "InvalidateRect", hwnd, 0, 1)
			}
		}
		return 0
	case wmCaptureChanged, wmCancelMode:
		if app.dragging {
			app.closeSelection(false)
		}
		return 0
	case wmRightDown, wmClose:
		app.closeSelection(false)
		return 0
	case wmKeyDown:
		if w == 27 {
			app.closeSelection(false)
		}
		return 0
	case wmPaint:
		var ps paint
		dc := call(user, "BeginPaint", hwnd, uintptr(unsafe.Pointer(&ps)))
		if !app.selection.Empty() {
			r := app.selection.Sub(app.desktop.Min)
			pen := call(gdi, "CreatePen", 0, 4, 0x00ff8000)
			oldPen := call(gdi, "SelectObject", dc, pen)
			oldBrush := call(gdi, "SelectObject", dc, call(gdi, "GetStockObject", 5))
			call(gdi, "Rectangle", dc, uintptr(r.Min.X), uintptr(r.Min.Y), uintptr(r.Max.X), uintptr(r.Max.Y))
			call(gdi, "SelectObject", dc, oldBrush)
			call(gdi, "SelectObject", dc, oldPen)
			call(gdi, "DeleteObject", pen)
		}
		call(user, "EndPaint", hwnd, uintptr(unsafe.Pointer(&ps)))
		return 0
	}
	return call(user, "DefWindowProcW", hwnd, uintptr(id), w, l)
}

func cursor() image.Point {
	var pt point
	call(user, "GetCursorPos", uintptr(unsafe.Pointer(&pt)))
	return image.Pt(int(pt.x), int(pt.y))
}

func (a *application) closeSelection(accepted bool) {
	a.dragging, a.selecting = false, false
	if a.overlay != 0 {
		hwnd := a.overlay
		a.overlay = 0
		call(user, "DestroyWindow", hwnd)
	}
	if !accepted {
		call(user, "ShowWindow", a.window, 5)
		a.status("打开报告，点「开始框选」并拖出截图区域。")
		return
	}
	a.beginCapture()
}

func (a *application) beginCapture() {
	r := a.selection
	if r.Dx()*r.Dy() > 48_000_000 {
		call(user, "ShowWindow", a.window, 5)
		message(a.window, "框选区域太大，请缩小范围。", 0x30)
		return
	}
	center := r.Min.Add(r.Size().Div(2))
	position := uintptr(uint64(uint32(center.X)) | uint64(uint32(center.Y))<<32)
	target := call(user, "WindowFromPoint", position)
	if target != 0 {
		call(user, "SetForegroundWindow", call(user, "GetAncestor", target, 2))
	}
	a.saveHotkey = call(user, "RegisterHotKey", a.window, saveKey, 0x4006, 'S') != 0
	a.cancelHotkey = call(user, "RegisterHotKey", a.window, cancelKey, 0x4006, 'X') != 0
	if !a.placeControls(r) && (!a.saveHotkey || !a.cancelHotkey) {
		a.releaseHotkeys()
		call(user, "ShowWindow", a.window, 5)
		message(a.window, "截图区域遮住了控制窗，结束快捷键也被其他应用占用。请缩小框选范围。", 0x30)
		return
	}
	a.grabber = newScreenGrabber(r)
	if a.grabber == nil {
		a.releaseHotkeys()
		call(user, "ShowWindow", a.window, 5)
		message(a.window, "无法抓取所选区域。", 0x10)
		return
	}
	a.stitch = stitcher{}
	a.misses = 0
	a.capturing = true
	a.controls(true)
	a.status("在原页面慢慢往下滚。结束可按 Ctrl+Shift+S。")
	call(dwm, "DwmFlush")
	a.capture()
	if a.capturing && call(user, "SetTimer", a.window, timerID, 45, 0) == 0 {
		a.finish(false)
		message(a.window, "无法启动连续截图。", 0x10)
	}
}

func (a *application) placeControls(selection image.Rectangle) bool {
	var window rect
	call(user, "GetWindowRect", a.window, uintptr(unsafe.Pointer(&window)))
	size := window.image().Size()
	bounds := rect{int32(selection.Min.X), int32(selection.Min.Y), int32(selection.Max.X), int32(selection.Max.Y)}
	monitor := call(user, "MonitorFromRect", uintptr(unsafe.Pointer(&bounds)), 2)
	info := monitorInfo{}
	info.size = uint32(unsafe.Sizeof(info))
	area := a.desktop
	if call(user, "GetMonitorInfoW", monitor, uintptr(unsafe.Pointer(&info))) != 0 {
		area = info.work.image()
	}
	corners := []image.Point{
		area.Min,
		image.Pt(area.Max.X-size.X, area.Min.Y),
		image.Pt(area.Min.X, area.Max.Y-size.Y),
		area.Max.Sub(size),
	}
	for _, p := range corners {
		candidate := image.Rectangle{Min: p, Max: p.Add(size)}
		if candidate.In(area) && !candidate.Overlaps(selection.Inset(-10)) {
			call(user, "SetWindowPos", a.window, ^uintptr(0), uintptr(p.X), uintptr(p.Y), 0, 0, 0x51)
			return true
		}
	}
	return false
}

func (a *application) releaseHotkeys() {
	if a.saveHotkey {
		call(user, "UnregisterHotKey", a.window, saveKey)
	}
	if a.cancelHotkey {
		call(user, "UnregisterHotKey", a.window, cancelKey)
	}
	a.saveHotkey, a.cancelHotkey = false, false
}

func (a *application) capture() {
	im := a.grabber.frame()
	if im == nil {
		a.finish(false)
		message(a.window, "屏幕抓取中断，请重新开始。", 0x10)
		return
	}
	switch a.stitch.append(im) {
	case added, unchanged:
		a.misses = 0
		if a.stitch.scrolled() {
			a.status("已拼接 " + strconv.Itoa(a.stitch.height) + " 像素")
		}
	case mismatch:
		a.misses++
		if a.misses >= 3 {
			a.status("画面未对齐，请慢慢滚回上一段再继续。")
		}
	case full:
		a.misses = max(a.misses, 1)
		a.finish(true)
	}
}

func (a *application) finish(save bool) {
	if !a.capturing {
		return
	}
	a.capturing = false
	call(user, "KillTimer", a.window, timerID)
	a.releaseHotkeys()
	a.grabber.close()
	a.grabber = nil
	a.controls(false)
	call(user, "ShowWindow", a.window, 5)
	a.status("打开报告，点「开始框选」并拖出截图区域。")
	defer func() { a.stitch = stitcher{} }()
	if !save {
		return
	}
	if !a.stitch.scrolled() {
		message(a.window, "没有拼接到滚动内容。请重新框选并分几次向下滚动。", 0x30)
		return
	}
	name := "LShot.png"
	if a.misses > 0 {
		name = "LShot-未完整.png"
		message(a.window, "截图未完整，只能保存已拼接的部分。", 0x30)
	}
	path, err := choosePath(a.window, name)
	if err == nil && path != "" {
		err = writePNG(path, a.stitch.image())
	}
	if err != nil {
		message(a.window, "保存失败："+err.Error(), 0x10)
	} else if path != "" {
		a.status("已保存 " + filepath.Base(path))
	}
}

func message(owner uintptr, description string, style uintptr) {
	call(user, "MessageBoxW", owner, uintptr(unsafe.Pointer(text(description))), uintptr(unsafe.Pointer(text("LShot"))), style)
}

func choosePath(owner uintptr, name string) (string, error) {
	var file [32768]uint16
	value, _ := syscall.UTF16FromString(name)
	copy(file[:], value)
	filter := []uint16{'P', 'N', 'G', ' ', '图', '片', 0, '*', '.', 'p', 'n', 'g', 0, 0}
	extension, _ := syscall.UTF16PtrFromString("png")
	options := saveDialog{
		owner: owner, filter: &filter[0], filterIndex: 1, file: &file[0], maxFile: uint32(len(file)),
		flags: 0x0008000e, defaultExtension: extension,
	}
	options.size = uint32(unsafe.Sizeof(options))
	if call(dialog, "GetSaveFileNameW", uintptr(unsafe.Pointer(&options))) == 0 {
		if call(dialog, "CommDlgExtendedError") != 0 {
			return "", errors.New("无法打开保存对话框")
		}
		return "", nil
	}
	return syscall.UTF16ToString(file[:]), nil
}

func writePNG(path string, im image.Image) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".lshot-*.png")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	err = encoder.Encode(file, im)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}

type screenGrabber struct {
	region                      image.Rectangle
	screen, memory, bitmap, old uintptr
	pixels                      unsafe.Pointer
}

func newScreenGrabber(region image.Rectangle) *screenGrabber {
	g := &screenGrabber{region: region, screen: call(user, "GetDC", 0)}
	if g.screen == 0 {
		return nil
	}
	g.memory = call(gdi, "CreateCompatibleDC", g.screen)
	if g.memory == 0 {
		g.close()
		return nil
	}
	info := bitmapHeader{size: 40, width: int32(region.Dx()), height: -int32(region.Dy()), planes: 1, bits: 32}
	g.bitmap = call(gdi, "CreateDIBSection", g.memory, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&g.pixels)), 0, 0)
	if g.bitmap == 0 || g.pixels == nil {
		g.close()
		return nil
	}
	g.old = call(gdi, "SelectObject", g.memory, g.bitmap)
	if g.old == 0 || g.old == ^uintptr(0) {
		g.old = 0
		g.close()
		return nil
	}
	return g
}

func (g *screenGrabber) frame() *image.RGBA {
	r := g.region
	if call(gdi, "BitBlt", g.memory, 0, 0, uintptr(r.Dx()), uintptr(r.Dy()), g.screen,
		uintptr(r.Min.X), uintptr(r.Min.Y), 0x40cc0020) == 0 {
		return nil
	}
	call(gdi, "GdiFlush")
	im := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	pixels := unsafe.Slice((*byte)(g.pixels), len(im.Pix))
	for i := 0; i < len(pixels); i += 4 {
		im.Pix[i], im.Pix[i+1], im.Pix[i+2], im.Pix[i+3] = pixels[i+2], pixels[i+1], pixels[i], 255
	}
	return im
}

func (g *screenGrabber) close() {
	if g.old != 0 {
		call(gdi, "SelectObject", g.memory, g.old)
	}
	if g.bitmap != 0 {
		call(gdi, "DeleteObject", g.bitmap)
	}
	if g.memory != 0 {
		call(gdi, "DeleteDC", g.memory)
	}
	if g.screen != 0 {
		call(user, "ReleaseDC", 0, g.screen)
	}
}
