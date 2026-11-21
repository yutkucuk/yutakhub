// yutakhub access: hands a locked game's access code to people who hold a role on a Discord server.
//
// The app signs the player in with Discord (scopes: identify, guilds.members.read) and sends the
// player's own access token here. This worker asks Discord two things with that token: is it a token
// of OUR application, and which roles does its owner have on OUR server. If the role is there, the
// answer is the access code of the game. Nothing is stored; the codes live in this worker's secrets.
//
// Settings (Workers > Settings > Variables and Secrets):
//   CLIENT_ID   the Discord application's id
//   GUILD_ID    the server's id
//   ROLE_ID     the role that may play (several: separated by commas)
//   KEYS        secret, JSON: {"<game id>": "<access code>"}

const API = "https://discord.com/api/v10";
// Discord lets one person ask for their own server membership only a few times in a short while, and the
// app and the game both ask within seconds of each other. So an answer is kept for a few minutes per
// sign-in (in Cloudflare's cache, under a hash of the token: the token itself is never stored), and a
// "slow down" from Discord is waited out once.
const KEEP = 300;

async function sha(s) {
  const d = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(s));
  return [...new Uint8Array(d)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

// a Discord GET, waiting out one 429 (Discord says how long in retry_after)
async function ask(path, h) {
  for (let i = 0; i < 2; i++) {
    const r = await fetch(API + path, { headers: h });
    if (r.status !== 429 || i === 1) return r;
    let wait = 1.5;
    try { wait = Number((await r.clone().json()).retry_after) || Number(r.headers.get("retry-after")) || 1.5; } catch (e) {}
    console.log("discord 429 on", path.split("/").slice(0, 3).join("/"), "waiting", wait);
    if (wait > 8) return r;
    await new Promise((ok) => setTimeout(ok, Math.ceil(wait * 1000) + 100));
  }
}

// who the token belongs to and which roles they have: {status, error?, user?, roles?}
async function lookup(auth, env, ctx) {
  const key = new Request("https://cache.yutakhub.invalid/" + (await sha(auth + "|" + env.GUILD_ID)));
  const hit = await caches.default.match(key);
  if (hit) return await hit.json();
  const h = { authorization: auth, "user-agent": "yutakhub-access (https://github.com/yutkucuk/yutakhub, 2)" };
  let out;
  const me = await ask("/oauth2/@me", h);
  if (me.status === 401) out = { status: 401, error: "login" };
  else if (!me.ok) out = { status: me.status === 429 ? 429 : 502, error: me.status === 429 ? "busy" : "discord", d: me.status };
  else {
    const info = await me.json();
    if (!info.application || String(info.application.id) !== String(env.CLIENT_ID) || !(info.scopes || []).includes("guilds.members.read")) out = { status: 401, error: "login" };
    else {
      const mem = await ask("/users/@me/guilds/" + env.GUILD_ID + "/member", h);
      if (mem.status === 401) out = { status: 401, error: "login" };
      else if (mem.status === 404) out = { status: 403, error: "nomember" };
      else if (mem.status === 429) out = { status: 429, error: "busy" };
      else if (!mem.ok) out = { status: 502, error: "discord", d: mem.status };
      else out = { status: 200, user: info.user ? String(info.user.username || "") : "", roles: ((await mem.json()).roles || []).map(String) };
    }
  }
  console.log("lookup", out.status, out.error || "", out.d || "");
  // answers that say something about the person are kept; "try again" answers are not
  if (out.status === 200 || out.status === 403 || out.status === 401) {
    ctx.waitUntil(caches.default.put(key, new Response(JSON.stringify(out), { headers: { "cache-control": "max-age=" + KEEP } })));
  }
  return out;
}

function reply(status, body) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json; charset=utf-8", "cache-control": "no-store" },
  });
}

export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    if (request.method === "GET" && url.pathname === "/") return reply(200, { ok: true, service: "yutakhub access" });
    if (request.method !== "POST" || url.pathname !== "/key") return reply(404, { error: "notfound" });

    const auth = request.headers.get("authorization") || "";
    if (!auth.startsWith("Bearer ") || auth.length < 20 || auth.length > 300) return reply(401, { error: "login" });
    let game = "";
    try { game = String((await request.json()).game || ""); } catch (e) { return reply(400, { error: "bad" }); }
    let keys = {};
    try { keys = JSON.parse(env.KEYS || "{}"); } catch (e) { return reply(500, { error: "config" }); }
    if (!game || !Object.prototype.hasOwnProperty.call(keys, game)) return reply(404, { error: "nogame" });

    const who = await lookup(auth, env, ctx);
    if (who.status !== 200) return reply(who.status, { error: who.error });
    const want = String(env.ROLE_ID || "").split(",").map((s) => s.trim()).filter(Boolean);
    if (!want.some((r) => who.roles.includes(r))) return reply(403, { error: "norole" });
    const info = { user: { username: who.user } };
    return reply(200, { code: String(keys[game]), user: info.user ? String(info.user.username || "") : "" });
  },
};
