package main

// A locked game whose access goes by a Discord role (catalog: mod.lock.auth). Instead of typing an
// access code, the player signs in with Discord in their own browser; a small service (access/worker.js
// in this repository) looks at their roles on the owner's server and, if the role is there, hands the
// access code to the app. The code is used for this one install and not kept: take the role away and
// the next version does not install.
//
// The sign-in is Discord's implicit grant: the browser comes back to a page this app serves on
// 127.0.0.1 for a moment, with the token in the address's fragment. No secret of the application is
// involved, so nothing in this program is worth stealing.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

type LockAuth struct {
	Type     string `json:"type"`     // "discord"
	Client   string `json:"client"`   // the Discord application's id
	Endpoint string `json:"endpoint"` // where the access code is asked for (https)
}

type DiscordLogin struct {
	Token string `json:"token,omitempty"`
	Exp   int64  `json:"exp,omitempty"` // unix time after which the token is no use
}

const discordPort = 53682 // registered with the Discord application as http://127.0.0.1:53682/cb

const discordPage = `<!doctype html><meta charset="utf-8"><title>yutakhub</title>
<body style="font:18px/1.5 system-ui,sans-serif;background:#12131c;color:#ebe8de;display:grid;place-items:center;height:100vh;margin:0">
<div id="m" style="max-width:32em;text-align:center">…</div>
<script>
const p = new URLSearchParams(location.hash.slice(1)), q = new URLSearchParams(location.search), m = document.getElementById("m");
const tr = (navigator.language || "").toLowerCase().startsWith("tr");
if (p.get("access_token")) {
  fetch("/token", { method: "POST", body: JSON.stringify({ token: p.get("access_token"), state: p.get("state"), expires: p.get("expires_in") }) })
    .then((r) => { m.textContent = r.ok ? (tr ? "Giriş tamam. Bu sekmeyi kapatıp yutakhub'a dönebilirsin." : "Signed in. You can close this tab and go back to yutakhub.") : (tr ? "Giriş doğrulanamadı. yutakhub'dan yeniden dene." : "The sign-in could not be confirmed. Try again from yutakhub."); });
  history.replaceState(null, "", "/cb");
} else {
  fetch("/token", { method: "POST", body: JSON.stringify({ error: p.get("error") || q.get("error") || "none", state: p.get("state") || q.get("state") }) });
  m.textContent = tr ? "Giriş iptal edildi. yutakhub'dan yeniden deneyebilirsin." : "The sign-in was cancelled. You can try again from yutakhub.";
}
</script>`

// discordSignIn opens the player's browser at Discord and waits for them to come back.
func discordSignIn(client string) (*DiscordLogin, error) {
	if t := os.Getenv("YUTAKHUB_DISCORD_TOKEN"); t != "" { // development: a token handed in from outside
		return &DiscordLogin{Token: t, Exp: time.Now().Add(time.Hour).Unix()}, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(discordPort))
	if err != nil {
		return nil, coded("login", "the sign-in page could not be opened on this computer: "+err.Error())
	}
	raw := make([]byte, 16)
	rand.Read(raw)
	state := hex.EncodeToString(raw)
	type result struct {
		l   *DiscordLogin
		err error
	}
	done := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/cb", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		io.WriteString(w, discordPage)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Token   string `json:"token"`
			State   string `json:"state"`
			Expires string `json:"expires"`
			Error   string `json:"error"`
		}
		b, _ := io.ReadAll(io.LimitReader(r.Body, 8<<10))
		if json.Unmarshal(b, &in) != nil || in.State != state {
			w.WriteHeader(400)
			return
		}
		if in.Token == "" {
			select {
			case done <- result{nil, coded("login", "sign-in cancelled")}:
			default:
			}
			return
		}
		secs, _ := strconv.Atoi(in.Expires)
		if secs <= 0 {
			secs = 3600
		}
		select {
		case done <- result{&DiscordLogin{Token: in.Token, Exp: time.Now().Unix() + int64(secs) - 300}, nil}:
		default:
		}
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		srv.Shutdown(ctx)
		cancel()
	}()
	q := url.Values{}
	q.Set("response_type", "token")
	q.Set("client_id", client)
	q.Set("scope", "identify guilds.members.read")
	q.Set("redirect_uri", fmt.Sprintf("http://127.0.0.1:%d/cb", discordPort))
	q.Set("state", state)
	if err := openTarget("https://discord.com/oauth2/authorize?" + q.Encode()); err != nil {
		return nil, coded("login", err)
	}
	select {
	case r := <-done:
		time.Sleep(300 * time.Millisecond) // let the page get its answer before the door closes
		return r.l, r.err
	case <-time.After(4 * time.Minute):
		return nil, coded("login", "nobody signed in")
	}
}

var errLoginStale = errors.New("the Discord sign-in is no longer valid")

// askKey asks the access service for the game's code with the player's token.
func askKey(auth *LockAuth, token, game string) (string, error) {
	if len(auth.Endpoint) < 9 || auth.Endpoint[:8] != "https://" {
		if os.Getenv("YUTAKHUB_DISCORD_TOKEN") == "" || len(auth.Endpoint) < 17 || auth.Endpoint[:17] != "http://127.0.0.1:" {
			return "", coded("verify", "the access service's address is not https")
		}
	}
	body, _ := json.Marshal(map[string]string{"game": game})
	req, _ := http.NewRequest("POST", auth.Endpoint, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "yutakhub/"+appVersion())
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", coded("download", err)
	}
	defer resp.Body.Close()
	var out struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	json.Unmarshal(b, &out)
	switch {
	case resp.StatusCode == 200 && out.Code != "":
		return out.Code, nil
	case resp.StatusCode == 401:
		return "", errLoginStale
	case resp.StatusCode == 403 && out.Error == "nomember":
		return "", coded("nomember", "")
	case resp.StatusCode == 403:
		return "", coded("norole", "")
	}
	return "", coded("download", fmt.Sprintf("access service: HTTP %d %s", resp.StatusCode, out.Error))
}

// discordKey gets the game's access code for the player: signs them in if need be, and once more
// if the stored sign-in turned out to be stale.
func (a *App) discordKey(g *Game, progress func(string, int)) (string, error) {
	auth := g.Mod.Lock.Auth
	for try := 0; try < 2; try++ {
		a.mu.Lock()
		l := a.state.Discord
		a.mu.Unlock()
		if l == nil || l.Token == "" || time.Now().Unix() >= l.Exp {
			progress("login", 0)
			nl, err := discordSignIn(auth.Client)
			if err != nil {
				return "", err
			}
			a.mu.Lock()
			a.state.Discord = nl
			a.saveState()
			a.mu.Unlock()
			l = nl
		}
		progress("access", 0)
		code, err := askKey(auth, l.Token, g.ID)
		if err == errLoginStale {
			a.mu.Lock()
			a.state.Discord = nil
			a.saveState()
			a.mu.Unlock()
			continue
		}
		return code, err
	}
	return "", coded("login", "the Discord sign-in was not accepted")
}
