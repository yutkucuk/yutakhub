package main

// The publisher is the developer's side of yutakhub: it takes a mod straight from the
// developer's own game folder, packages it and puts it on GitHub together with the
// updated catalog, so every installed yutakhub offers it as an update.

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Publish struct {
	Include          []string `json:"include,omitempty"`
	Exclude          []string `json:"exclude,omitempty"`
	VersionFile      string   `json:"versionFile,omitempty"`
	VersionRegex     string   `json:"versionRegex,omitempty"`
	VersionUTF16     bool     `json:"versionUtf16,omitempty"`
	GameVersionRegex string   `json:"gameVersionRegex,omitempty"`
}

type pubConfig struct {
	Token string `json:"token"`
	Repo  string `json:"repo"`
}

func (a *App) pubConfigPath() string { return filepath.Join(a.dataDir, "publisher.json") }

func (a *App) pubConfig() pubConfig {
	var c pubConfig
	if b, err := os.ReadFile(a.pubConfigPath()); err == nil {
		json.Unmarshal(b, &c)
	}
	if c.Repo == "" {
		c.Repo = repo
	}
	return c
}

func (a *App) publisherOn() bool {
	return publisherFlag || exists(a.pubConfigPath())
}

var repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func (a *App) pubSave(repoName, token string) error {
	repoName = strings.TrimSpace(repoName)
	repoName = strings.TrimSuffix(strings.TrimPrefix(repoName, "https://github.com/"), "/")
	if !repoRe.MatchString(repoName) {
		return coded("pub_repo", repoName)
	}
	c := a.pubConfig()
	c.Repo = repoName
	if t := strings.TrimSpace(token); t != "" {
		c.Token = t
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(a.pubConfigPath(), b, 0o600)
}

// ---------- GitHub ----------

func ghAPI() string {
	if b := os.Getenv("YUTAKHUB_GH_API"); b != "" {
		return b
	}
	return "https://api.github.com"
}

func ghPath(p string) string {
	parts := strings.Split(p, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

func (c pubConfig) gh(method, endpoint string, body any, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, ghAPI()+endpoint, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "yutakhub/"+appVersion())
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		json.Unmarshal(data, &e)
		return resp.StatusCode, fmt.Errorf("GitHub %d: %s", resp.StatusCode, e.Message)
	}
	if out != nil {
		return resp.StatusCode, json.Unmarshal(data, out)
	}
	return resp.StatusCode, nil
}

type ghFile struct {
	SHA     string `json:"sha"`
	Content string `json:"content"`
}

func (c pubConfig) getFile(p string) (*ghFile, error) {
	var f ghFile
	code, err := c.gh("GET", "/repos/"+c.Repo+"/contents/"+ghPath(p)+"?ref="+url.QueryEscape(branch), nil, &f)
	if code == 404 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (c pubConfig) putFile(p string, data []byte, message string) error {
	body := map[string]any{"message": message, "content": base64.StdEncoding.EncodeToString(data), "branch": branch}
	old, err := c.getFile(p)
	if err != nil {
		return err
	}
	if old != nil {
		body["sha"] = old.SHA
	}
	_, err = c.gh("PUT", "/repos/"+c.Repo+"/contents/"+ghPath(p), body, nil)
	return err
}

func (a *App) pubTest() error {
	c := a.pubConfig()
	if c.Token == "" || c.Repo == "" {
		return coded("pub_no_token", "")
	}
	var r struct {
		Permissions struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	if _, err := c.gh("GET", "/repos/"+c.Repo, nil, &r); err != nil {
		return coded("pub_github", err)
	}
	if !r.Permissions.Push {
		return coded("pub_github", "this token cannot write to "+c.Repo)
	}
	return nil
}

// ---------- collecting a mod from the game folder ----------

func globMatch(pattern, rel string) bool {
	pattern, rel = strings.ToLower(pattern), strings.ToLower(rel)
	if strings.HasSuffix(pattern, "/**") {
		return strings.HasPrefix(rel, strings.TrimSuffix(pattern, "**"))
	}
	ok, _ := path.Match(pattern, rel)
	return ok
}

func collectFiles(root string, p *Publish) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(full string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		r, err := filepath.Rel(root, full)
		if err != nil {
			return nil
		}
		rel := filepath.ToSlash(r)
		inc := false
		for _, pat := range p.Include {
			inc = inc || globMatch(pat, rel)
		}
		for _, pat := range p.Exclude {
			if globMatch(pat, rel) {
				inc = false
			}
		}
		if inc {
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// buildPackage zips the mod's files and returns the zip plus a fingerprint of the contents.
func buildPackage(root string, files []string) ([]byte, string, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	h := sha256.New()
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, "", err
		}
		fmt.Fprintf(h, "%s\x00%s\n", rel, sha256Hex(data))
		w, err := zw.CreateHeader(&zip.FileHeader{Name: rel, Method: zip.Deflate, Modified: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			return nil, "", err
		}
		w.Write(data)
	}
	if err := zw.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), hex.EncodeToString(h.Sum(nil)), nil
}

func detectVersion(file, expr string, utf16 bool) string {
	if file == "" || expr == "" {
		return ""
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	if utf16 {
		b = bytes.ReplaceAll(b, []byte{0}, nil)
	}
	if m := re.FindSubmatch(b); len(m) > 1 {
		return string(m[1])
	}
	return ""
}

type PubGame struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Published   string `json:"published"`
	Local       string `json:"local"`
	GameVersion string `json:"gameVersion"`
	State       string `json:"state"` // same | changed | nogame | nomod | nospec | nooriginal
	Files       int    `json:"files"`
	NotesTr     string `json:"notesTr"`
	NotesEn     string `json:"notesEn"`
}

type PubState struct {
	Repo     string    `json:"repo"`
	HasToken bool      `json:"hasToken"`
	Games    []PubGame `json:"games"`
}

func (a *App) pubGame(g *Game) PubGame {
	m := &g.Mod
	v := PubGame{ID: g.ID, Name: g.Name, Type: m.Type, Published: m.Version, NotesTr: m.Notes["tr"], NotesEn: m.Notes["en"]}
	root := a.gamePath(g)
	p := m.Publish
	switch {
	case root == "":
		v.State = "nogame"
	case p == nil:
		v.State = "nospec"
	case m.Type == "extract":
		files, _ := collectFiles(root, p)
		v.Files = len(files)
		if len(files) == 0 || (m.Detect != "" && !exists(filepath.Join(root, filepath.FromSlash(m.Detect)))) {
			v.State = "nomod"
			return v
		}
		v.Local = detectVersion(filepath.Join(root, filepath.FromSlash(p.VersionFile)), p.VersionRegex, p.VersionUTF16)
		_, content, err := buildPackage(root, files)
		if err == nil && content == m.Content {
			v.State = "same"
		} else {
			v.State = "changed"
		}
	case m.Type == "patch":
		target := filepath.Join(root, filepath.FromSlash(m.Target))
		live, orig := a.fileSha(target), a.fileSha(m.backupPath(root))
		if live == "" || orig == "" || live == orig {
			v.State = "nooriginal"
			return v
		}
		v.Local = detectVersion(target, p.VersionRegex, false)
		v.GameVersion = detectVersion(target, p.GameVersionRegex, false)
		v.State = "changed"
		for _, pt := range m.Patches {
			if strings.EqualFold(pt.To, live) {
				v.State = "same"
			}
		}
	}
	return v
}

func (a *App) pubState() PubState {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.pubConfig()
	s := PubState{Repo: c.Repo, HasToken: c.Token != "", Games: []PubGame{}}
	if a.catalog != nil {
		for i := range a.catalog.Games {
			s.Games = append(s.Games, a.pubGame(&a.catalog.Games[i]))
		}
	}
	return s
}

var versionRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,3}$`)

func marshalCatalog(c *Catalog) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	enc.Encode(c)
	return buf.Bytes()
}

func (a *App) publish(id, version, gameVersion, notesTr, notesEn string, progress func(string, int)) error {
	version, gameVersion = strings.TrimSpace(version), strings.TrimSpace(gameVersion)
	if !versionRe.MatchString(version) {
		return coded("pub_version", version)
	}
	c := a.pubConfig()
	if c.Token == "" || c.Repo == "" {
		return coded("pub_no_token", "")
	}
	a.mu.Lock()
	local := a.findGame(id)
	root := ""
	if local != nil {
		root = a.gamePath(local)
	}
	a.mu.Unlock()
	if local == nil || root == "" {
		return coded("nogame", id)
	}

	// always edit the catalog that is on GitHub right now, not our cached copy
	progress("pub_catalog", 10)
	cf, err := c.getFile("catalog.json")
	if err != nil {
		return coded("pub_github", err)
	}
	if cf == nil {
		return coded("pub_github", "catalog.json is not in "+c.Repo+" yet")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(cf.Content, "\n", ""))
	var cat Catalog
	if err == nil {
		err = json.Unmarshal(raw, &cat)
	}
	if err != nil {
		return coded("pub_github", "catalog.json on GitHub is not valid: "+err.Error())
	}
	var g *Game
	for i := range cat.Games {
		if cat.Games[i].ID == id {
			g = &cat.Games[i]
		}
	}
	if g == nil || g.Mod.Publish == nil {
		return coded("pub_github", "catalog.json on GitHub has no publish settings for "+id)
	}
	m := &g.Mod

	progress("pub_package", 30)
	var pkg []byte
	var pkgPath string
	switch m.Type {
	case "extract":
		files, err := collectFiles(root, m.Publish)
		if err != nil || len(files) == 0 {
			return coded("pub_nomod", g.Name)
		}
		data, content, err := buildPackage(root, files)
		if err != nil {
			return coded("write", err)
		}
		if content != m.Content {
			if version == m.Version {
				return coded("pub_same_version", version)
			}
			pkg, pkgPath = data, fmt.Sprintf("mods/%s/%s-%s.zip", id, id, version)
			m.URL, m.SHA256, m.Content = pkgPath, sha256Hex(data), content
		}
	case "patch":
		target := filepath.Join(root, filepath.FromSlash(m.Target))
		orig, err1 := os.ReadFile(m.backupPath(root))
		mod, err2 := os.ReadFile(target)
		if err1 != nil || err2 != nil || bytes.Equal(orig, mod) {
			return coded("pub_nooriginal", g.Name)
		}
		from, to := sha256Hex(orig), sha256Hex(mod)
		known := false
		for _, pt := range m.Patches {
			known = known || strings.EqualFold(pt.To, to)
		}
		if !known {
			if version == m.Version {
				return coded("pub_same_version", version)
			}
			if gameVersion == "" {
				return coded("pub_version", "game version")
			}
			pkg = makePatch(orig, mod)
			if _, err := applyPatch(orig, pkg); err != nil {
				return coded("verify", err)
			}
			pkgPath = fmt.Sprintf("mods/%s/%s-%s-game-%s.yhp", id, id, version, gameVersion)
			for _, pt := range m.Patches { // installs made with older builds must still be offered the update
				if !inList(m.KnownModded, pt.To) {
					m.KnownModded = append(m.KnownModded, pt.To)
				}
			}
			m.Patches = []Patch{{GameVersion: gameVersion, From: from, To: to, URL: pkgPath}}
		}
	default:
		return coded("app_too_old", m.Type)
	}
	m.Version = version
	if m.Notes == nil {
		m.Notes = LStr{}
	}
	m.Notes["tr"], m.Notes["en"] = strings.TrimSpace(notesTr), strings.TrimSpace(notesEn)

	if pkg != nil {
		progress("pub_upload", 50)
		if err := c.putFile(pkgPath, pkg, fmt.Sprintf("%s %s %s", g.Name, m.Name, version)); err != nil {
			return coded("pub_github", err)
		}
	}
	progress("pub_catalog", 80)
	out := marshalCatalog(&cat)
	if err := c.putFile("catalog.json", out, fmt.Sprintf("catalog: %s %s %s", g.Name, m.Name, version)); err != nil {
		return coded("pub_github", err)
	}
	progress("pub_catalog", 100)
	if repo == "" || strings.EqualFold(repo, c.Repo) { // show the result right away instead of waiting for GitHub's cache
		a.mu.Lock()
		a.catalog, a.catalogErr = &cat, ""
		writeFileAtomic(filepath.Join(a.dataDir, "catalog.json"), out)
		a.mu.Unlock()
	}
	return nil
}
