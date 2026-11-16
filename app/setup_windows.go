//go:build windows

package main

// Installing yutakhub. The setup exe and the installed app are the same program: started from
// anywhere but its install folder it offers to install itself, per user, without admin rights.

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

const uninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\yutakhub`

func installDir() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "yutakhub")
}
func installedExe() string { return filepath.Join(installDir(), "yutakhub.exe") }
func startMenuLink() string {
	return filepath.Join(os.Getenv("APPDATA"), `Microsoft\Windows\Start Menu\Programs`, "yutakhub.lnk")
}

func isInstalledCopy() bool {
	exe, err := os.Executable()
	return err == nil && strings.EqualFold(filepath.Clean(exe), filepath.Clean(installedExe()))
}

func installedVersion() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, uninstallKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, _ := k.GetStringValue("DisplayVersion")
	if !exists(installedExe()) {
		return ""
	}
	return v
}

func setupInfo() map[string]any {
	return map[string]any{"dir": installDir(), "installed": installedVersion()}
}

func powershell(script string, env ...string) error {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	return cmd.Run()
}

// makeShortcut creates a .lnk to target. An empty link path means "on the desktop".
func makeShortcut(link, target string) error {
	script := `$l=$env:YH_LNK; if(-not $l){$l=[IO.Path]::Combine([Environment]::GetFolderPath('Desktop'),'yutakhub.lnk')};` +
		`$s=(New-Object -ComObject WScript.Shell).CreateShortcut($l);$s.TargetPath=$env:YH_EXE;` +
		`$s.WorkingDirectory=[IO.Path]::GetDirectoryName($env:YH_EXE);$s.Description='yutakhub';$s.Save()`
	return powershell(script, "YH_LNK="+link, "YH_EXE="+target)
}

func createShortcut() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return makeShortcut("", exe)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func registerUninstall() {
	exe := installedExe()
	k, _, err := registry.CreateKey(registry.CURRENT_USER, uninstallKey, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	k.SetStringValue("DisplayName", "yutakhub")
	k.SetStringValue("DisplayVersion", appVersion())
	k.SetStringValue("Publisher", "yutakhub")
	k.SetStringValue("DisplayIcon", exe)
	k.SetStringValue("InstallLocation", installDir())
	k.SetStringValue("UninstallString", `"`+exe+`" --uninstall`)
	k.SetDWordValue("NoModify", 1)
	k.SetDWordValue("NoRepair", 1)
	if st, err := os.Stat(exe); err == nil {
		k.SetDWordValue("EstimatedSize", uint32(st.Size()/1024))
	}
}

func setupInstall(desktop bool, progress func(string, int)) error {
	src, err := os.Executable()
	if err != nil {
		return err
	}
	dst := installedExe()
	progress("install", 10)
	if err := os.MkdirAll(installDir(), 0o755); err != nil {
		return err
	}
	if exists(dst) { // a running copy cannot be overwritten, but it can be moved aside
		os.Remove(dst + ".old")
		if err := os.Rename(dst, dst+".old"); err != nil {
			return err
		}
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	if repo == "" { // test builds read their mod list from the folder they sit in
		from := filepath.Dir(src)
		copyFile(filepath.Join(from, "catalog.json"), filepath.Join(installDir(), "catalog.json"))
		filepath.Walk(filepath.Join(from, "mods"), func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				rel, _ := filepath.Rel(from, p)
				copyFile(p, filepath.Join(installDir(), rel))
			}
			return nil
		})
	}
	progress("install", 50)
	if err := makeShortcut(startMenuLink(), dst); err != nil {
		return fmt.Errorf("start menu shortcut: %w", err)
	}
	if desktop {
		makeShortcut("", dst)
	}
	progress("install", 85)
	registerUninstall()
	progress("install", 100)
	return exec.Command(dst, "--updated").Start()
}

// runUninstall removes yutakhub itself. Mods already installed into games are left alone.
func runUninstall() {
	tr := systemLang() == "tr"
	q := "Remove yutakhub from this PC?\n\nMods you installed stay in your games. To remove those too, use \"Remove mod\" in yutakhub first."
	done := "yutakhub was removed."
	if tr {
		q = "yutakhub bu bilgisayardan kaldırılsın mı?\n\nKurduğun modlar oyunlarda kalır. Onları da kaldırmak istiyorsan önce yutakhub içinden \"Modu kaldır\"ı kullan."
		done = "yutakhub kaldırıldı."
	}
	if messageBoxFlags(q, 0x4|0x20) != 6 {
		return
	}
	os.Remove(startMenuLink())
	powershell(`Remove-Item ([IO.Path]::Combine([Environment]::GetFolderPath('Desktop'),'yutakhub.lnk')) -ErrorAction SilentlyContinue`)
	registry.DeleteKey(registry.CURRENT_USER, uninstallKey)
	if dir, err := os.UserConfigDir(); err == nil {
		os.RemoveAll(filepath.Join(dir, "yutakhub"))
	}
	messageBoxFlags(done, 0x40)
	// a program cannot delete its own exe while running, so a tiny helper does it a moment later
	cmd := exec.Command("cmd", "/c", "ping -n 3 127.0.0.1 >nul & rmdir /s /q \""+installDir()+"\"")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	cmd.Dir = os.TempDir()
	cmd.Start()
}
