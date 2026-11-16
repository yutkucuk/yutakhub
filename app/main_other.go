//go:build !windows

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

// Non-Windows builds are for development: the same backend and UI, served to a normal browser.

func steamRoots() []string {
	if p := os.Getenv("YUTAKHUB_STEAM"); p != "" {
		return []string{p}
	}
	return nil
}
func fileInUse(string) bool          { return os.Getenv("YUTAKHUB_FAKE_RUNNING") != "" }
func openTarget(string) error        { return nil }
func pickFolderDialog() string       { return os.Getenv("YUTAKHUB_PICK") }
func systemLang() string             { return "en" }
func startDetached(exe string) error { return exec.Command(exe).Start() }
func createShortcut() error          { return nil }
func setupInfo() map[string]any      { return map[string]any{"dir": "/opt/yutakhub", "installed": ""} }
func setupInstall(desktop bool, progress func(string, int)) error {
	progress("install", 100)
	return nil
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "mkpatch" {
		if err := cmdMkpatch(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	for _, a := range os.Args[1:] {
		if a == "--publisher" {
			publisherFlag = true
		}
		if a == "--setup" {
			appMode = "setup"
		}
	}
	app := newApp()
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if appMode == "setup" {
			io.WriteString(w, setupHTML)
			return
		}
		io.WriteString(w, uiHTML)
	})
	http.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		res, err := app.Call(strings.TrimPrefix(r.URL.Path, "/api/"), string(body))
		if err != nil {
			w.WriteHeader(500)
			io.WriteString(w, err.Error())
			return
		}
		json.NewEncoder(w).Encode(res)
	})
	addr := "127.0.0.1:8777"
	fmt.Println("dev server on http://" + addr)
	http.ListenAndServe(addr, nil)
}
