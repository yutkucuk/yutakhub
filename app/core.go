package main

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed version.txt
var versionRaw string

//go:embed ui/index.html
var uiHTML string

//go:embed ui/setup.html
var setupHTML string

// repo is "owner/name" on GitHub and branch its default branch. The release build sets both
// with -X main.repo=... -X main.branch=... A build without a repo is a test build: it reads
// catalog.json and the mods folder that sit next to the exe and never updates itself.
var (
	repo   = ""
	branch = "main"
)

// appMode is "app" normally and "setup" when the exe is running as the installer.
var appMode = "app"

// publisherFlag is set by --publisher and shows the developer's Publish tab.
var publisherFlag = false

func appVersion() string { return strings.TrimSpace(versionRaw) }

// ---------- catalog ----------

type LStr map[string]string

type Patch struct {
	GameVersion string `json:"gameVersion"`
	From        string `json:"from"`
	To          string `json:"to"`
	URL         string `json:"url"`
}

type Mod struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Summary LStr   `json:"summary"`
	Notes   LStr   `json:"notes"`
	Type    string `json:"type"` // "extract" or "patch"
	Detect  string `json:"detect,omitempty"`

	// extract
	URL     string   `json:"url,omitempty"`
	SHA256  string   `json:"sha256,omitempty"`
	Content string   `json:"content,omitempty"` // fingerprint of the packaged files, written by the publisher
	Keep    []string `json:"keep,omitempty"`
	Remove  []string `json:"remove,omitempty"`

	// patch
	Target      string   `json:"target,omitempty"`
	Backup      string   `json:"backup,omitempty"`
	Patches     []Patch  `json:"patches,omitempty"`
	KnownModded []string `json:"knownModded,omitempty"`

	// how the publisher collects this mod from the developer's own game folder
	Publish *Publish `json:"publish,omitempty"`
}

type Game struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	SteamAppID int    `json:"steamAppId"`
	Exe        string `json:"exe"`
	Image      string `json:"image,omitempty"`
	// Standalone marks a complete game that yutakhub installs into its own folder
	// (no Steam game to find): the package is the game, and Exe is started directly.
	Standalone bool `json:"standalone,omitempty"`
	Mod        Mod  `json:"mod"`
}

type Catalog struct {
	Schema int    `json:"schema"`
	MinApp string `json:"minApp"`
	Games  []Game `json:"games"`
}

const catalogSchema = 1

// ---------- state on disk ----------

type GameState struct {
	Path      string   `json:"path,omitempty"`
	Version   string   `json:"version,omitempty"`
	Files     []string `json:"files,omitempty"`
	TargetSha string   `json:"targetSha,omitempty"`
}

type Settings struct {
	AutoUpdateMods bool `json:"autoUpdateMods"`
	CloseOnPlay    bool `json:"closeOnPlay"`
	NoUpdateCheck  bool `json:"noUpdateCheck"`
}

type State struct {
	Lang     string                `json:"lang"`
	Settings Settings              `json:"settings"`
	Games    map[string]*GameState `json:"games"`
}

// ---------- errors the UI can translate ----------

type codedErr struct {
	Code   string
	Detail string
}

func (e *codedErr) Error() string { return e.Code + ": " + e.Detail }
func coded(code string, detail any) error {
	return &codedErr{code, fmt.Sprint(detail)}
}

// ---------- app ----------

type Op struct {
	Seq     int    `json:"seq"`
	Running bool   `json:"running"`
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Phase   string `json:"phase"`
	Pct     int    `json:"pct"`
	ErrCode string `json:"errCode"`
	Err     string `json:"err"`
}

type hashEntry struct {
	size  int64
	mtime time.Time
	sha   string
}

type App struct {
	mu         sync.Mutex
	dataDir    string
	state      State
	catalog    *Catalog
	catalogErr string
	op         Op
	hashes     map[string]hashEntry
	updVersion string
	updSha     string
	updChecked bool
	lastCheck  int64
	onQuit     func()
}

func (a *App) logf(format string, args ...any) {
	f, err := os.OpenFile(filepath.Join(a.dataDir, "yutakhub.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s  %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

func newApp() *App {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	if d := os.Getenv("YUTAKHUB_DATA"); d != "" {
		dir = d
	} else {
		dir = filepath.Join(dir, "yutakhub")
	}
	os.MkdirAll(dir, 0o755)
	a := &App{dataDir: dir, hashes: map[string]hashEntry{}}
	a.state.Games = map[string]*GameState{}
	if b, err := os.ReadFile(filepath.Join(dir, "state.json")); err == nil {
		json.Unmarshal(b, &a.state)
		if a.state.Games == nil {
			a.state.Games = map[string]*GameState{}
		}
	}
	if a.state.Lang != "tr" && a.state.Lang != "en" {
		a.state.Lang = systemLang()
	}
	if b, err := os.ReadFile(filepath.Join(dir, "catalog.json")); err == nil {
		var c Catalog
		if json.Unmarshal(b, &c) == nil && c.Schema == catalogSchema {
			a.catalog = &c
		}
	}
	if exe, err := selfPath(); err == nil {
		os.Remove(exe + ".old")
		os.Remove(exe + ".new")
	}
	if st, err := os.Stat(filepath.Join(dir, "yutakhub.log")); err == nil && st.Size() > 1<<20 {
		os.Remove(filepath.Join(dir, "yutakhub.log"))
	}
	a.logf("yutakhub %s started (repo %q)", appVersion(), repo)
	return a
}

func selfPath() (string, error) {
	if p := os.Getenv("YUTAKHUB_SELF"); p != "" {
		return p, nil
	}
	return os.Executable()
}

func (a *App) saveState() {
	b, _ := json.MarshalIndent(a.state, "", "  ")
	writeFileAtomic(filepath.Join(a.dataDir, "state.json"), b)
}

func (a *App) gs(id string) *GameState {
	g := a.state.Games[id]
	if g == nil {
		g = &GameState{}
		a.state.Games[id] = g
	}
	return g
}

// ---------- fetching ----------

func catalogBase() string {
	if b := os.Getenv("YUTAKHUB_BASE"); b != "" {
		return b
	}
	if repo == "" {
		if exe, err := os.Executable(); err == nil {
			return filepath.Dir(exe)
		}
		return ""
	}
	return "https://raw.githubusercontent.com/" + repo + "/" + branch + "/"
}

func releaseBase() string {
	if b := os.Getenv("YUTAKHUB_RELEASE_BASE"); b != "" {
		return b
	}
	if repo == "" {
		return ""
	}
	return "https://github.com/" + repo + "/releases/latest/download/"
}

var httpClient = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	ResponseHeaderTimeout: 30 * time.Second,
	TLSHandshakeTimeout:   20 * time.Second,
}}

// openRef opens a file named by the catalog: an https URL, or a path relative to base.
// base may be an https URL or (for local testing) a folder.
func openRef(base, ref string) (io.ReadCloser, int64, error) {
	full := ref
	if !strings.Contains(ref, "://") {
		if base == "" {
			return nil, 0, errors.New("no download source configured")
		}
		if !strings.Contains(base, "://") {
			f, err := os.Open(filepath.Join(base, filepath.FromSlash(ref)))
			if err != nil {
				return nil, 0, err
			}
			st, _ := f.Stat()
			return f, st.Size(), nil
		}
		if !strings.HasSuffix(base, "/") {
			base += "/"
		}
		full = base + strings.ReplaceAll(ref, " ", "%20")
	}
	if !strings.HasPrefix(full, "https://") {
		return nil, 0, errors.New("only https downloads are allowed: " + full)
	}
	req, _ := http.NewRequest("GET", full, nil)
	req.Header.Set("User-Agent", "yutakhub/"+appVersion())
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("%s: HTTP %d", full, resp.StatusCode)
	}
	return resp.Body, resp.ContentLength, nil
}

func download(base, ref string, limit int64, progress func(pct int)) ([]byte, error) {
	r, size, err := openRef(base, ref)
	if err != nil {
		return nil, coded("download", err)
	}
	defer r.Close()
	var buf bytes.Buffer
	chunk := make([]byte, 64<<10)
	for {
		n, err := r.Read(chunk)
		buf.Write(chunk[:n])
		if int64(buf.Len()) > limit {
			return nil, coded("download", "file is larger than expected")
		}
		if progress != nil && size > 0 {
			progress(int(int64(buf.Len()) * 100 / size))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, coded("download", err)
		}
	}
	return buf.Bytes(), nil
}

func (a *App) loadCatalog() {
	b, err := download(catalogBase(), "catalog.json", 4<<20, nil)
	var c Catalog
	if err == nil {
		if e := json.Unmarshal(b, &c); e != nil {
			err = e
		} else if c.Schema != catalogSchema {
			err = coded("app_too_old", "")
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastCheck = time.Now().Unix()
	if err != nil {
		a.logf("catalog: %v", err)
		a.catalogErr = err.Error()
		var ce *codedErr
		if errors.As(err, &ce) && ce.Code == "app_too_old" {
			a.catalogErr = "app_too_old"
		}
		return
	}
	a.catalogErr = ""
	a.catalog = &c
	writeFileAtomic(filepath.Join(a.dataDir, "catalog.json"), b)
}

// ---------- versions ----------

func verLess(x, y string) bool {
	xs, ys := strings.Split(x, "."), strings.Split(y, ".")
	for i := 0; i < len(xs) || i < len(ys); i++ {
		var xi, yi int
		if i < len(xs) {
			xi, _ = strconv.Atoi(strings.TrimLeft(xs[i], "vV"))
		}
		if i < len(ys) {
			yi, _ = strconv.Atoi(strings.TrimLeft(ys[i], "vV"))
		}
		if xi != yi {
			return xi < yi
		}
	}
	return false
}

// ---------- finding games ----------

var (
	vdfPath    = regexp.MustCompile(`"path"\s+"([^"]*)"`)
	acfInstDir = regexp.MustCompile(`"installdir"\s+"([^"]*)"`)
)

func steamLibraries() []string {
	var libs []string
	seen := map[string]bool{}
	add := func(p string) {
		p = filepath.Clean(p)
		k := strings.ToLower(p)
		if !seen[k] {
			seen[k] = true
			libs = append(libs, p)
		}
	}
	for _, root := range steamRoots() {
		add(root)
		b, err := os.ReadFile(filepath.Join(root, "steamapps", "libraryfolders.vdf"))
		if err != nil {
			continue
		}
		for _, m := range vdfPath.FindAllStringSubmatch(string(b), -1) {
			add(strings.ReplaceAll(m[1], `\\`, `\`))
		}
	}
	return libs
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func (a *App) gamePath(g *Game) string {
	if g.Standalone {
		return filepath.Join(a.dataDir, "games", g.ID)
	}
	if st := a.state.Games[g.ID]; st != nil && st.Path != "" {
		if exists(filepath.Join(st.Path, g.Exe)) {
			return st.Path
		}
	}
	for _, lib := range steamLibraries() {
		b, err := os.ReadFile(filepath.Join(lib, "steamapps", fmt.Sprintf("appmanifest_%d.acf", g.SteamAppID)))
		if err != nil {
			continue
		}
		if m := acfInstDir.FindStringSubmatch(string(b)); m != nil {
			p := filepath.Join(lib, "steamapps", "common", m[1])
			if exists(filepath.Join(p, g.Exe)) {
				return p
			}
		}
	}
	return ""
}

// ---------- status ----------

type GameView struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Custom     bool     `json:"custom"`
	AppID      int      `json:"appId"`
	Image      string   `json:"image"`
	ModName    string   `json:"modName"`
	Summary    LStr     `json:"summary"`
	Notes      LStr     `json:"notes"`
	Latest     string   `json:"latest"`
	Installed  string   `json:"installed"`
	Standalone bool     `json:"standalone"`
	Status     string   `json:"status"` // nogame | notinstalled | installed | update | unsupported
	Path       string   `json:"path"`
	Supported  []string `json:"supported"`
}

func (a *App) fileSha(p string) string {
	st, err := os.Stat(p)
	if err != nil {
		return ""
	}
	if h, ok := a.hashes[p]; ok && h.size == st.Size() && h.mtime.Equal(st.ModTime()) {
		return h.sha
	}
	sha, err := sha256File(p)
	if err != nil {
		return ""
	}
	a.hashes[p] = hashEntry{st.Size(), st.ModTime(), sha}
	return sha
}

func inList(l []string, s string) bool {
	for _, x := range l {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func (m *Mod) backupPath(root string) string {
	t := filepath.Join(root, filepath.FromSlash(m.Target))
	name := m.Backup
	if name == "" {
		name = filepath.Base(t) + ".yutakhub-backup"
	}
	return filepath.Join(filepath.Dir(t), name)
}

// patchSource says which file (live target or backup) can be patched, and with which patch.
func (a *App) patchSource(m *Mod, root string) (string, *Patch) {
	for _, p := range []string{filepath.Join(root, filepath.FromSlash(m.Target)), m.backupPath(root)} {
		sha := a.fileSha(p)
		if sha == "" {
			continue
		}
		for i := range m.Patches {
			if strings.EqualFold(m.Patches[i].From, sha) {
				return p, &m.Patches[i]
			}
		}
	}
	return "", nil
}

func (a *App) view(g *Game) GameView {
	m := &g.Mod
	v := GameView{ID: g.ID, Name: g.Name, AppID: g.SteamAppID, Image: g.Image, ModName: m.Name,
		Summary: m.Summary, Notes: m.Notes, Latest: m.Version, Status: "nogame"}
	root := a.gamePath(g)
	if root == "" {
		return v
	}
	v.Path = root
	v.Standalone = g.Standalone
	st := a.state.Games[g.ID]
	v.Custom = st != nil && st.Path != "" && strings.EqualFold(filepath.Clean(st.Path), filepath.Clean(root))
	recorded := ""
	if st != nil {
		recorded = st.Version
	}
	switch m.Type {
	case "extract":
		if m.Detect == "" || !exists(filepath.Join(root, filepath.FromSlash(m.Detect))) {
			v.Status = "notinstalled"
			return v
		}
		v.Installed = recorded
	case "patch":
		for _, p := range m.Patches {
			v.Supported = append(v.Supported, p.GameVersion)
		}
		live := a.fileSha(filepath.Join(root, filepath.FromSlash(m.Target)))
		isTo, isFrom := false, false
		for _, p := range m.Patches {
			isTo = isTo || strings.EqualFold(p.To, live)
			isFrom = isFrom || strings.EqualFold(p.From, live)
		}
		switch {
		case isTo:
			v.Installed = m.Version
		case isFrom:
			v.Status = "notinstalled"
			return v
		case live != "" && (inList(m.KnownModded, live) || (st != nil && strings.EqualFold(st.TargetSha, live))):
			// an older build of the mod; updatable only if the original file is still around
			if src, _ := a.patchSource(m, root); src == "" {
				v.Status = "unsupported"
				return v
			}
			v.Installed = recorded
		default:
			v.Status = "unsupported"
			return v
		}
	default:
		v.Status = "unsupported"
		return v
	}
	if v.Installed == m.Version {
		v.Status = "installed"
	} else {
		v.Status = "update"
	}
	return v
}

func (a *App) findGame(id string) *Game {
	if a.catalog == nil {
		return nil
	}
	for i := range a.catalog.Games {
		if a.catalog.Games[i].ID == id {
			return &a.catalog.Games[i]
		}
	}
	return nil
}

// ---------- file helpers ----------

func writeFileAtomic(dest string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".yhtmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// safeRel cleans a path from a zip or the catalog and rejects anything that would leave the game folder.
func safeRel(name string) (string, bool) {
	name = strings.ReplaceAll(name, `\`, "/")
	c := path.Clean(name)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") || strings.HasPrefix(c, "/") || strings.Contains(c, ":") {
		return "", false
	}
	return c, true
}

func matchAny(patterns []string, rel string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(strings.ToLower(p), strings.ToLower(rel)); ok {
			return true
		}
	}
	return false
}

func removeAndPrune(root, rel string) {
	rel, ok := safeRel(rel)
	if !ok {
		return
	}
	p := filepath.Join(root, filepath.FromSlash(rel))
	os.RemoveAll(p)
	for d := filepath.Dir(p); len(d) > len(root) && strings.HasPrefix(d, root); d = filepath.Dir(d) {
		if os.Remove(d) != nil { // fails when not empty
			break
		}
	}
}

func (a *App) checkNotRunning(g *Game, root string) error {
	if fileInUse(filepath.Join(root, g.Exe)) {
		return coded("game_running", g.Name)
	}
	return nil
}

// ---------- install / uninstall ----------

func (a *App) install(g *Game, progress func(phase string, pct int)) error {
	a.mu.Lock()
	root := a.gamePath(g)
	a.mu.Unlock()
	if root == "" {
		return coded("nogame", g.Name)
	}
	if err := a.checkNotRunning(g, root); err != nil {
		return err
	}
	m := &g.Mod
	switch m.Type {
	case "extract":
		return a.installExtract(g, root, progress)
	case "patch":
		return a.installPatch(g, root, progress)
	}
	return coded("app_too_old", m.Type)
}

func (a *App) installExtract(g *Game, root string, progress func(string, int)) error {
	m := &g.Mod
	data, err := download(catalogBase(), m.URL, 1<<30, func(p int) { progress("download", p) })
	if err != nil {
		return err
	}
	if m.SHA256 != "" && !strings.EqualFold(sha256Hex(data), m.SHA256) {
		return coded("verify", "downloaded file does not match the catalog")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return coded("verify", err)
	}
	progress("install", 0)
	var written []string
	for i, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rel, ok := safeRel(f.Name)
		if !ok {
			return coded("verify", "unsafe path in package: "+f.Name)
		}
		dest := filepath.Join(root, filepath.FromSlash(rel))
		if matchAny(m.Keep, rel) && exists(dest) {
			continue // the player's own settings stay
		}
		rc, err := f.Open()
		if err != nil {
			return coded("verify", err)
		}
		b, err := io.ReadAll(io.LimitReader(rc, 1<<30))
		rc.Close()
		if err != nil {
			return coded("verify", err)
		}
		if err := writeFileAtomic(dest, b); err != nil {
			if fileInUse(dest) || fileInUse(filepath.Join(root, g.Exe)) {
				return coded("game_running", g.Name)
			}
			return coded("write", err)
		}
		written = append(written, rel)
		progress("install", (i+1)*100/len(zr.File))
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.gs(g.ID)
	for _, old := range st.Files { // files a previous version had that this one no longer ships
		if !inList(written, old) && !matchAny(m.Keep, old) {
			removeAndPrune(root, old)
		}
	}
	sort.Strings(written)
	st.Files, st.Version = written, m.Version
	a.saveState()
	return nil
}

func (a *App) installPatch(g *Game, root string, progress func(string, int)) error {
	m := &g.Mod
	target := filepath.Join(root, filepath.FromSlash(m.Target))
	a.mu.Lock()
	src, p := a.patchSource(m, root)
	a.mu.Unlock()
	if p == nil {
		return coded("unsupported", g.Name)
	}
	patch, err := download(catalogBase(), p.URL, 1<<30, func(pc int) { progress("download", pc) })
	if err != nil {
		return err
	}
	progress("install", 10)
	orig, err := os.ReadFile(src)
	if err != nil {
		return coded("write", err)
	}
	out, err := applyPatch(orig, patch)
	if err != nil {
		return coded("verify", err)
	}
	if !strings.EqualFold(sha256Hex(out), p.To) {
		return coded("verify", "patched file does not match the catalog")
	}
	progress("install", 60)
	backup := m.backupPath(root)
	if src == target { // keep the untouched original next to it
		if err := writeFileAtomic(backup, orig); err != nil {
			return coded("write", err)
		}
	}
	if err := writeFileAtomic(target, out); err != nil {
		if fileInUse(target) {
			return coded("game_running", g.Name)
		}
		return coded("write", err)
	}
	progress("install", 100)
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.gs(g.ID)
	st.Version, st.TargetSha, st.Files = m.Version, strings.ToLower(p.To), nil
	a.saveState()
	return nil
}

func (a *App) uninstall(g *Game, progress func(string, int)) error {
	a.mu.Lock()
	root := a.gamePath(g)
	a.mu.Unlock()
	if root == "" {
		return coded("nogame", g.Name)
	}
	if err := a.checkNotRunning(g, root); err != nil {
		return err
	}
	m := &g.Mod
	progress("uninstall", 0)
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.gs(g.ID)
	switch m.Type {
	case "extract":
		for _, f := range append(append([]string{}, st.Files...), m.Remove...) {
			if matchAny(m.Keep, f) && !inList(m.Remove, f) {
				continue
			}
			removeAndPrune(root, f)
		}
		if m.Detect != "" && exists(filepath.Join(root, filepath.FromSlash(m.Detect))) {
			return coded("game_running", g.Name)
		}
	case "patch":
		target, backup := filepath.Join(root, filepath.FromSlash(m.Target)), m.backupPath(root)
		if !exists(backup) {
			return coded("no_backup", g.Name)
		}
		b, err := os.ReadFile(backup)
		if err != nil {
			return coded("write", err)
		}
		if err := writeFileAtomic(target, b); err != nil {
			return coded("write", err)
		}
		os.Remove(backup)
	}
	st.Version, st.Files, st.TargetSha = "", nil, ""
	a.saveState()
	return nil
}

// ---------- app self-update ----------

func (a *App) checkAppUpdate() {
	base := releaseBase()
	if base == "" {
		return
	}
	b, err := download(base, "version.json", 1<<20, nil)
	if err != nil {
		a.logf("update check: %v", err)
		return
	}
	var v struct {
		Version string `json:"version"`
		SHA256  string `json:"sha256"`
	}
	if json.Unmarshal(b, &v) != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.updChecked = true
	if verLess(appVersion(), v.Version) {
		a.updVersion, a.updSha = v.Version, v.SHA256
	} else {
		a.updVersion, a.updSha = "", ""
	}
}

func (a *App) applyAppUpdate(progress func(string, int)) error {
	a.mu.Lock()
	want := a.updSha
	a.mu.Unlock()
	exe, err := selfPath()
	if err != nil {
		return coded("write", err)
	}
	data, err := download(releaseBase(), "yutakhub.exe", 1<<30, func(p int) { progress("download", p) })
	if err != nil {
		return err
	}
	if want == "" || !strings.EqualFold(sha256Hex(data), want) {
		return coded("verify", "downloaded update does not match the published checksum")
	}
	progress("install", 50)
	if err := os.WriteFile(exe+".new", data, 0o755); err != nil {
		return coded("self_write", err)
	}
	os.Remove(exe + ".old")
	if err := os.Rename(exe, exe+".old"); err != nil { // allowed on Windows even while running
		os.Remove(exe + ".new")
		return coded("self_write", err)
	}
	if err := os.Rename(exe+".new", exe); err != nil {
		os.Rename(exe+".old", exe)
		return coded("self_write", err)
	}
	progress("restart", 100)
	if err := startDetached(exe); err == nil && a.onQuit != nil {
		time.AfterFunc(600*time.Millisecond, a.onQuit)
	}
	return nil
}

// ---------- operations (run in the background; the UI polls) ----------

func (a *App) run(kind, id string, fn func(progress func(string, int)) error) error {
	a.mu.Lock()
	if a.op.Running {
		a.mu.Unlock()
		return coded("busy", "")
	}
	a.op = Op{Seq: a.op.Seq + 1, Running: true, Kind: kind, ID: id, Phase: "start"}
	a.mu.Unlock()
	go func() {
		err := fn(func(phase string, pct int) {
			a.mu.Lock()
			a.op.Phase, a.op.Pct = phase, pct
			a.mu.Unlock()
		})
		a.mu.Lock()
		a.op.Running = false
		a.op.Seq++
		if kind != "refresh" {
			a.logf("%s %s: %v", kind, id, err)
		}
		if err != nil {
			var ce *codedErr
			if errors.As(err, &ce) {
				a.op.ErrCode, a.op.Err = ce.Code, ce.Detail
			} else {
				a.op.ErrCode, a.op.Err = "error", err.Error()
			}
		}
		a.mu.Unlock()
	}()
	return nil
}

type fullState struct {
	AppVersion   string     `json:"appVersion"`
	Lang         string     `json:"lang"`
	Repo         string     `json:"repo"`
	Games        []GameView `json:"games"`
	CatalogError string     `json:"catalogError"`
	Op           Op         `json:"op"`
	Update       string     `json:"update"`
	UpdChecked   bool       `json:"updChecked"`
	MustUpdate   bool       `json:"mustUpdate"`
	Settings     Settings   `json:"settings"`
	LastCheck    int64      `json:"lastCheck"`
	DataDir      string     `json:"dataDir"`
	Publisher    bool       `json:"publisher"`
	Mode         string     `json:"mode"`
}

func (a *App) snapshot() fullState {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := fullState{AppVersion: appVersion(), Lang: a.state.Lang, Repo: repo, CatalogError: a.catalogErr,
		Op: a.op, Update: a.updVersion, UpdChecked: a.updChecked, Games: []GameView{},
		Settings: a.state.Settings, LastCheck: a.lastCheck, DataDir: a.dataDir,
		Publisher: a.publisherOn(), Mode: appMode}
	if a.catalog != nil {
		for i := range a.catalog.Games {
			s.Games = append(s.Games, a.view(&a.catalog.Games[i]))
		}
		s.MustUpdate = a.catalog.MinApp != "" && verLess(appVersion(), a.catalog.MinApp)
	}
	if a.catalogErr == "app_too_old" {
		s.MustUpdate = true
	}
	return s
}

// Call is the single entry point the UI uses.
func (a *App) Call(name, argsJSON string) (any, error) {
	var args struct {
		ID          string `json:"id"`
		Lang        string `json:"lang"`
		Key         string `json:"key"`
		Value       bool   `json:"value"`
		Repo        string `json:"repo"`
		Token       string `json:"token"`
		Version     string `json:"version"`
		GameVersion string `json:"gameVersion"`
		NotesTr     string `json:"notesTr"`
		NotesEn     string `json:"notesEn"`
		Desktop     bool   `json:"desktop"`
	}
	if argsJSON != "" {
		json.Unmarshal([]byte(argsJSON), &args)
	}
	game := func() (*Game, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if g := a.findGame(args.ID); g != nil {
			return g, nil
		}
		return nil, errors.New("unknown game")
	}
	switch name {
	case "state":
		return a.snapshot(), nil
	case "op":
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.op, nil
	case "refresh":
		return nil, a.run("refresh", "", func(func(string, int)) error {
			a.loadCatalog()
			a.mu.Lock()
			skip := a.state.Settings.NoUpdateCheck
			a.mu.Unlock()
			if !skip {
				a.checkAppUpdate()
			}
			return nil
		})
	case "checkUpdate":
		return nil, a.run("checkupdate", "", func(func(string, int)) error {
			if releaseBase() == "" {
				return nil
			}
			a.mu.Lock()
			a.updChecked = false
			a.mu.Unlock()
			a.checkAppUpdate()
			a.mu.Lock()
			defer a.mu.Unlock()
			if !a.updChecked {
				return coded("download", "version.json")
			}
			return nil
		})
	case "setSetting":
		a.mu.Lock()
		defer a.mu.Unlock()
		switch args.Key {
		case "autoUpdateMods":
			a.state.Settings.AutoUpdateMods = args.Value
		case "closeOnPlay":
			a.state.Settings.CloseOnPlay = args.Value
		case "noUpdateCheck":
			a.state.Settings.NoUpdateCheck = args.Value
		}
		a.saveState()
		return nil, nil
	case "resetFolder":
		a.mu.Lock()
		defer a.mu.Unlock()
		if st := a.state.Games[args.ID]; st != nil {
			st.Path = ""
			a.saveState()
		}
		return nil, nil
	case "openData":
		return nil, openTarget(a.dataDir)
	case "shortcut":
		return nil, a.run("shortcut", "", func(func(string, int)) error {
			if err := createShortcut(); err != nil {
				return coded("write", err)
			}
			return nil
		})
	case "enablePublisher":
		publisherFlag = true
		if !exists(a.pubConfigPath()) {
			os.WriteFile(a.pubConfigPath(), []byte("{}"), 0o600)
		}
		return nil, nil
	case "pubState":
		return a.pubState(), nil
	case "pubSave":
		return nil, a.pubSave(args.Repo, args.Token)
	case "pubTest":
		return nil, a.run("pubtest", "", func(func(string, int)) error { return a.pubTest() })
	case "publish":
		g, err := game()
		if err != nil {
			return nil, err
		}
		return nil, a.run("publish", g.ID, func(p func(string, int)) error {
			return a.publish(g.ID, args.Version, args.GameVersion, args.NotesTr, args.NotesEn, p)
		})
	case "setupInfo":
		return setupInfo(), nil
	case "setupInstall":
		return nil, a.run("setup", "", func(p func(string, int)) error {
			if err := setupInstall(args.Desktop, p); err != nil {
				return coded("write", err)
			}
			if a.onQuit != nil {
				time.AfterFunc(900*time.Millisecond, a.onQuit)
			}
			return nil
		})
	case "openRepo", "openIssues":
		if repo == "" {
			return nil, nil
		}
		if name == "openIssues" {
			return nil, openTarget("https://github.com/" + repo + "/issues")
		}
		return nil, openTarget("https://github.com/" + repo)
	case "install", "uninstall":
		g, err := game()
		if err != nil {
			return nil, err
		}
		return nil, a.run(name, g.ID, func(p func(string, int)) error {
			if name == "install" {
				return a.install(g, p)
			}
			return a.uninstall(g, p)
		})
	case "play":
		g, err := game()
		if err != nil {
			return nil, err
		}
		if g.Standalone {
			a.mu.Lock()
			exe := filepath.Join(a.gamePath(g), g.Exe)
			a.mu.Unlock()
			if !exists(exe) {
				return nil, coded("nogame", g.Name)
			}
			err = openTarget(exe)
		} else {
			err = openTarget(fmt.Sprintf("steam://rungameid/%d", g.SteamAppID))
		}
		a.mu.Lock()
		quit := a.state.Settings.CloseOnPlay && err == nil && a.onQuit != nil
		a.mu.Unlock()
		if quit {
			time.AfterFunc(1500*time.Millisecond, a.onQuit)
		}
		return nil, err
	case "openFolder":
		g, err := game()
		if err != nil {
			return nil, err
		}
		a.mu.Lock()
		p := a.gamePath(g)
		a.mu.Unlock()
		if p == "" {
			return nil, nil
		}
		return nil, openTarget(p)
	case "pickFolder":
		g, err := game()
		if err != nil {
			return nil, err
		}
		return nil, a.run("pick", g.ID, func(func(string, int)) error {
			dir := pickFolderDialog()
			if dir == "" {
				return nil
			}
			if !exists(filepath.Join(dir, g.Exe)) {
				return coded("wrong_folder", g.Exe)
			}
			a.mu.Lock()
			a.gs(g.ID).Path = dir
			a.saveState()
			a.mu.Unlock()
			return nil
		})
	case "setLang":
		if args.Lang == "tr" || args.Lang == "en" {
			a.mu.Lock()
			a.state.Lang = args.Lang
			a.saveState()
			a.mu.Unlock()
		}
		return nil, nil
	case "appUpdate":
		return nil, a.run("appupdate", "", a.applyAppUpdate)
	case "openReleases":
		if repo != "" {
			return nil, openTarget("https://github.com/" + repo + "/releases/latest")
		}
		return nil, nil
	}
	return nil, errors.New("unknown call: " + name)
}
