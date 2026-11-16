//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	ole32    = windows.NewLazySystemDLL("ole32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procMessageBox        = user32.NewProc("MessageBoxW")
	procFindWindow        = user32.NewProc("FindWindowW")
	procShowWindow        = user32.NewProc("ShowWindow")
	procSetForeground     = user32.NewProc("SetForegroundWindow")
	procShellExecute      = shell32.NewProc("ShellExecuteW")
	procBrowseForFolder   = shell32.NewProc("SHBrowseForFolderW")
	procGetPathFromIDList = shell32.NewProc("SHGetPathFromIDListW")
	procCoInitializeEx    = ole32.NewProc("CoInitializeEx")
	procCoUninitialize    = ole32.NewProc("CoUninitialize")
	procCoTaskMemFree     = ole32.NewProc("CoTaskMemFree")
	procGetUserDefaultUI  = kernel32.NewProc("GetUserDefaultUILanguage")
)

var mainWindow uintptr

func steamRoots() []string {
	var roots []string
	read := func(root registry.Key, path, name string) {
		k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
		if err != nil {
			return
		}
		defer k.Close()
		if v, _, err := k.GetStringValue(name); err == nil && v != "" {
			roots = append(roots, filepath.FromSlash(v))
		}
	}
	read(registry.CURRENT_USER, `Software\Valve\Steam`, "SteamPath")
	read(registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Valve\Steam`, "InstallPath")
	read(registry.LOCAL_MACHINE, `SOFTWARE\Valve\Steam`, "InstallPath")
	return roots
}

// fileInUse reports whether another program (the running game) has the file open.
func fileInUse(path string) bool {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err == nil {
		f.Close()
		return false
	}
	var errno syscall.Errno
	return errors.As(err, &errno) && (errno == windows.ERROR_SHARING_VIOLATION || errno == windows.ERROR_LOCK_VIOLATION)
}

// openTarget opens a folder, a web page or a steam:// link with whatever Windows uses for it.
func openTarget(target string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	t, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	r, _, _ := procShellExecute.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(t)), 0, 0, 1)
	if r <= 32 {
		return errors.New("could not open " + target)
	}
	return nil
}

func pickFolderDialog() string {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	procCoInitializeEx.Call(0, 2) // apartment threaded
	defer procCoUninitialize.Call()
	type browseInfo struct {
		Owner       uintptr
		Root        uintptr
		DisplayName *uint16
		Title       *uint16
		Flags       uint32
		Callback    uintptr
		LParam      uintptr
		Image       int32
	}
	display := make([]uint16, windows.MAX_PATH)
	title, _ := windows.UTF16PtrFromString("yutakhub")
	bi := browseInfo{Owner: mainWindow, DisplayName: &display[0], Title: title, Flags: 0x1 | 0x40 | 0x200}
	pidl, _, _ := procBrowseForFolder.Call(uintptr(unsafe.Pointer(&bi)))
	if pidl == 0 {
		return ""
	}
	defer procCoTaskMemFree.Call(pidl)
	buf := make([]uint16, 4096)
	if ok, _, _ := procGetPathFromIDList.Call(pidl, uintptr(unsafe.Pointer(&buf[0]))); ok == 0 {
		return ""
	}
	return windows.UTF16ToString(buf)
}

func systemLang() string {
	id, _, _ := procGetUserDefaultUI.Call()
	if id&0x3ff == 0x1f { // Turkish
		return "tr"
	}
	return "en"
}

func startDetached(exe string) error {
	return exec.Command(exe, "--updated").Start()
}

// alreadyRunning makes yutakhub single-instance: a second start just brings the open window forward.
func alreadyRunning() bool {
	name, _ := windows.UTF16PtrFromString("Local\\yutakhub-single-instance")
	tries := 1
	if len(os.Args) > 1 && os.Args[1] == "--updated" {
		tries = 25 // the old copy is still closing
	}
	for i := 0; i < tries; i++ {
		h, err := windows.CreateMutex(nil, false, name)
		if err == nil {
			return false
		}
		if h != 0 {
			windows.CloseHandle(h)
		}
		time.Sleep(200 * time.Millisecond)
	}
	class, _ := windows.UTF16PtrFromString("webview")
	title, _ := windows.UTF16PtrFromString("yutakhub")
	if hwnd, _, _ := procFindWindow.Call(uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title))); hwnd != 0 {
		procShowWindow.Call(hwnd, 9)
		procSetForeground.Call(hwnd)
	}
	return true
}

func messageBox(text string) { messageBoxFlags(text, 0x40) }

func messageBoxFlags(text string, flags uintptr) uintptr {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString("yutakhub")
	r, _, _ := procMessageBox.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), flags)
	return r
}

func webview2Installed() bool {
	const client = `\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`
	for _, loc := range []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node` + client},
		{registry.LOCAL_MACHINE, `SOFTWARE` + client},
		{registry.CURRENT_USER, `Software` + client},
	} {
		k, err := registry.OpenKey(loc.root, loc.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, _, err := k.GetStringValue("pv")
		k.Close()
		if err == nil && v != "" && v != "0.0.0.0" {
			return true
		}
	}
	return false
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "mkpatch" {
		if err := cmdMkpatch(os.Args[2:]); err != nil {
			messageBox(err.Error())
			os.Exit(1)
		}
		return
	}
	flags := map[string]bool{}
	for _, a := range os.Args[1:] {
		flags[a] = true
	}
	if flags["--uninstall"] {
		runUninstall()
		return
	}
	publisherFlag = flags["--publisher"]
	// Anywhere but its install folder, the released exe is the installer.
	if flags["--setup"] || (repo != "" && !isInstalledCopy() && !flags["--portable"]) {
		appMode = "setup"
	}
	if appMode == "app" && alreadyRunning() {
		return
	}
	runtime.LockOSThread()
	app := newApp()
	if appMode == "app" && isInstalledCopy() {
		registerUninstall() // keeps the version shown in Windows' app list right after a self-update
	}
	title, width, height, page := "yutakhub", uint(1120), uint(780), uiHTML
	if appMode == "setup" {
		title, width, height, page = "yutakhub Setup", 640, 500, setupHTML
	}

	if !webview2Installed() {
		messageBox("yutakhub needs Microsoft Edge WebView2, which is missing on this PC.\nThe download page will open now. Install it, then start yutakhub again.\n\n" +
			"yutakhub için Microsoft Edge WebView2 gerekiyor ve bu bilgisayarda yok.\nİndirme sayfası şimdi açılacak. Kurduktan sonra yutakhub'ı yeniden başlat.")
		openTarget("https://developer.microsoft.com/microsoft-edge/webview2/#download")
		return
	}

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  filepath.Join(app.dataDir, "webview"),
		WindowOptions: webview2.WindowOptions{
			Title: title, Width: width, Height: height, IconId: 2, Center: true,
		},
	})
	if w == nil {
		messageBox("The window could not be created. Reinstall Microsoft Edge WebView2 and try again.\n\nPencere açılamadı. Microsoft Edge WebView2'yi yeniden kurup tekrar dene.")
		return
	}
	defer w.Destroy()
	mainWindow = uintptr(w.Window())
	if appMode == "app" {
		w.SetSize(900, 620, webview2.HintMin)
	}
	app.onQuit = func() { w.Dispatch(func() { w.Terminate() }) }

	w.Bind("yhCall", func(name, args string) (string, error) {
		res, err := app.Call(name, args)
		if err != nil {
			return "", err
		}
		b, err := json.Marshal(res)
		return string(b), err
	})
	w.SetHtml(page)
	w.Run()
}
