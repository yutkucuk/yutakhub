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

function reply(status, body) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json; charset=utf-8", "cache-control": "no-store" },
  });
}

export default {
  async fetch(request, env) {
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

    const h = { authorization: auth, "user-agent": "yutakhub-access (https://github.com/yutkucuk/yutakhub, 1)" };
    // whose token is this, and was it issued to our application?
    const me = await fetch(API + "/oauth2/@me", { headers: h });
    if (me.status === 401) return reply(401, { error: "login" });
    if (!me.ok) return reply(502, { error: "discord", status: me.status });
    const info = await me.json();
    if (!info.application || String(info.application.id) !== String(env.CLIENT_ID)) return reply(401, { error: "login" });
    if (!(info.scopes || []).includes("guilds.members.read")) return reply(401, { error: "login" });

    const mem = await fetch(API + "/users/@me/guilds/" + env.GUILD_ID + "/member", { headers: h });
    if (mem.status === 401) return reply(401, { error: "login" });
    if (mem.status === 404) return reply(403, { error: "nomember" });
    if (mem.status === 429) return reply(429, { error: "busy" });
    if (!mem.ok) return reply(502, { error: "discord", status: mem.status });
    const roles = ((await mem.json()).roles || []).map(String);
    const want = String(env.ROLE_ID || "").split(",").map((s) => s.trim()).filter(Boolean);
    if (!want.some((r) => roles.includes(r))) return reply(403, { error: "norole" });

    return reply(200, { code: String(keys[game]), user: info.user ? String(info.user.username || "") : "" });
  },
};
