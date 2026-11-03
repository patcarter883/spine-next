"use strict";
// Spine dashboard. Plain JS, no build step; served with a strict CSP
// (no inline script), so every handler is attached here.
const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
const esc = s => String(s ?? "").replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const enc = encodeURIComponent;
const fmtN = n => (n ?? 0).toLocaleString();
const fmtUSD = n => "$" + (n ?? 0).toFixed(2);
const fmtMin = m => !m ? "—" : m < 1 ? Math.round(m * 60) + "s" : m < 90 ? Math.round(m) + "m" : m < 2880 ? (m / 60).toFixed(1) + "h" : (m / 1440).toFixed(1) + "d";
const fmtBytes = b => !b ? "empty" : b < 1024 ? b + " B" : b < 1 << 20 ? (b / 1024).toFixed(1) + " KiB" : b < 1 << 30 ? (b / (1 << 20)).toFixed(1) + " MiB" : (b / (1 << 30)).toFixed(1) + " GiB";
const lines = v => String(v || "").split(/[\n,]/).map(x => x.trim()).filter(Boolean);
function ago(d) { if (!d || d.startsWith("0001")) return "—"; const s = (Date.now() - new Date(d)) / 1000; if (s < 60) return Math.round(s) + "s"; if (s < 3600) return Math.round(s / 60) + "m"; if (s < 86400) return Math.round(s / 3600) + "h"; return Math.round(s / 86400) + "d" }
const tm = d => new Date(d).toLocaleTimeString([], { hour12: false });
const DONE = ["Integrated", "Failed", "Cancelled", "Planned"], STOPPED = ["Escalated", "Failed", "Cancelled"];

async function get(url) {
  const r = await fetch(url, { headers: { Accept: "application/json" } });
  if (r.status === 401) { showLogin(); throw new Error("sign in required") }
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || ("HTTP " + r.status));
  return j;
}
async function send(method, url, body) {
  const opt = { method, headers: { "X-Spine": "1" } };
  if (body !== undefined) { opt.headers["Content-Type"] = "application/json"; opt.body = JSON.stringify(body) }
  const r = await fetch(url, opt);
  if (r.status === 401 && url !== "/api/login") { showLogin(); throw new Error("sign in required") }
  const j = await r.json().catch(() => ({}));
  if (!r.ok) { const e = new Error(j.error || ("HTTP " + r.status)); e.body = j; throw e }
  return j;
}
function toast(msg, cls = "", onclick) {
  const t = document.createElement("div"); t.className = "toast " + cls; t.innerHTML = msg; $("#toasts").append(t);
  t.onclick = () => { t.remove(); onclick && onclick() }; setTimeout(() => t.remove(), 7000);
}
function dialog(html, bind) {
  $("#dlgBody").innerHTML = html; $("#dialog").hidden = false; bind && bind($("#dlgBody"));
  const f = $("#dlgBody textarea,#dlgBody input"); f && f.focus();
}
function closeDialog() { $("#dialog").hidden = true; $("#dlgBody").innerHTML = "" }
$("#dialog").onclick = e => { if (e.target.id === "dialog") closeDialog() };
document.addEventListener("keydown", e => { if (e.key === "Escape" && !$("#dialog").hidden) closeDialog() });

// - State -
let me = {}, ov = { multi: false, repos: [], tasks: [], notified: 0 }, repo = "all";
let route = { view: "tasks" }, lastNotified = null, unread = 0, es = null;
try { repo = localStorage.getItem("spine.repo") || "all" } catch (e) { }
const can = role => { const r = me.user?.role; const rank = { viewer: 1, operator: 2, admin: 3 }; return !me.read_only && (rank[r] || 0) >= rank[role] };
const repoNames = () => ov.repos.map(r => r.name);
const curRepo = () => repo !== "all" && repoNames().includes(repo) ? repo : (ov.repos[0]?.name || "");
const taskKey = t => t.repo + "/" + t.id;
const taskURL = (r, id, sub = "") => `/api/repos/${enc(r)}/tasks/${enc(id)}${sub}`;

const VIEWS = [["tasks", "Tasks"], ["plan", "Plan"], ["graph", "Graph"], ["dash", "Dashboard"], ["cost", "Costs"], ["quality", "Quality"],
  ["slots", "Supervisor"], ["health", "Health"], ["audit", "Audit"], ["settings", "Settings"]];

// - Routing: #/view[/repo/id[/tab]] -
function parseHash() {
  const h = location.hash.replace(/^#\/?/, "");
  if (h.startsWith("task=")) { const [r, id] = decodeURIComponent(h.slice(5)).split("/"); return { view: "tasks", repo: r, id, tab: "overview" } }
  const p = h.split("/").map(decodeURIComponent);
  const view = VIEWS.some(v => v[0] === p[0]) || p[0] === "new" ? p[0] : "tasks";
  return { view, repo: p[1], id: p[2], tab: p[3] || "overview" };
}
function go(path) { if (location.hash !== "#/" + path) location.hash = "#/" + path; else render() }
window.addEventListener("hashchange", () => { route = parseHash(); render() });

function renderNav() {
  $("#nav").innerHTML = VIEWS.map(([k, l]) => `<a href="#/${k}" class="${route.view === k ? "on" : ""}">${l}${k === "tasks" && ov.tasks.some(t => t.needs_you) ? ` <span class="badge b-Escalated">${ov.tasks.filter(t => t.needs_you).length}</span>` : ""}</a>`).join("");
  $("#newBtn").hidden = !can("operator");
  $("#who").innerHTML = me.user ? `${esc(me.user.name)} · ${esc(me.user.role)}${me.read_only ? " · read-only" : ""}${me.auth ? `<button id="logout">sign out</button>` : ""}` : "";
  const lo = $("#logout"); if (lo) lo.onclick = async () => { await send("POST", "/api/logout"); location.reload() };
}
function renderHeader() {
  const rs = ov.repos, c = $("#ctl"), selEl = $("#repoSel");
  if (ov.multi) {
    selEl.hidden = false;
    const opts = [`<option value="all">All repositories (${rs.length})</option>`].concat(rs.map(r => `<option value="${esc(r.name)}">${esc(r.name)}${r.state !== "running" ? " — " + esc(r.state) : ""}${r.paused ? " (paused)" : ""}</option>`)).join("");
    if (selEl.dataset.v !== opts) { selEl.innerHTML = opts; selEl.dataset.v = opts }
    if (repo !== "all" && !rs.some(r => r.name === repo)) repo = "all"; selEl.value = repo;
    const run = rs.filter(r => r.state === "running").length, bad = rs.filter(r => r.state === "broken").length;
    c.className = bad ? "off" : ""; c.innerHTML = `supervisor: <b>${run} running</b>${bad ? `, <b>${bad} broken</b>` : ""}`;
  } else {
    const r = rs[0] || {}; c.className = r.state === "running" ? "" : "off";
    c.innerHTML = r.state === "running" ? `controller <b>running</b>${r.paused ? " · <b>paused</b>" : ""}` : "controller <b>stopped</b> — run <code>spine controller</code>";
  }
  const broken = rs.filter(r => r.state === "broken" || r.error || r.paused);
  $("#banner").hidden = !broken.length;
  $("#banner").innerHTML = broken.map(r => r.paused && !r.error && r.state !== "broken"
    ? `<div><b>${esc(r.name)}</b> is paused${r.paused.by ? " by " + esc(r.paused.by) : ""}${r.paused.reason ? ": " + esc(r.paused.reason) : ""} — no new task starts.</div>`
    : `<div><b>${esc(r.name)}</b> ${esc(r.state)}${r.restarts ? ` (restarted ${r.restarts}×)` : ""}: ${esc((r.error || "").split("\n")[0]).slice(0, 240)}</div>`).join("");
  $("#bell").hidden = !me.notifications;
}
$("#repoSel").onchange = e => { repo = e.target.value; try { localStorage.setItem("spine.repo", repo) } catch (x) { } render() };
$("#newBtn").onclick = () => go("new");

// - Notifications -
async function pullNotifications() {
  if (!me.notifications) return;
  if (lastNotified === null) { lastNotified = ov.notified; return }
  if (ov.notified <= lastNotified) return;
  const feed = await get("/api/notifications?after=" + lastNotified).catch(() => []);
  lastNotified = ov.notified;
  for (const n of feed) {
    unread++;
    const cls = { failed: "bad", broken: "bad", escalated: "warn", approval: "warn", budget: "warn", integrated: "ok" }[n.kind] || "";
    const open = () => n.task ? go(`tasks/${n.repo}/${n.task}`) : null;
    toast(`<b>${esc(n.title)}</b>${n.message ? `<div class="muted">${esc(n.message)}</div>` : ""}`, cls, open);
    try {
      if (window.Notification && Notification.permission === "granted" && document.hidden) {
        const x = new Notification("Spine: " + n.title, { body: (n.repo ? "[" + n.repo + "] " : "") + (n.message || ""), tag: "spine-" + n.seq });
        x.onclick = () => { window.focus(); open() };
      }
    } catch (e) { }
  }
  $("#bellN").hidden = !unread; $("#bellN").textContent = unread;
}
$("#bell").onclick = async () => {
  unread = 0; $("#bellN").hidden = true;
  const feed = await get("/api/notifications").catch(() => []);
  const perm = window.Notification ? Notification.permission : "unsupported";
  dialog(`<h2>Notifications</h2>
   <p class="hint">Browser notifications: <b>${esc(perm)}</b> ${perm === "default" ? `<button class="btn small" id="perm">Enable</button>` : ""}. Webhooks, email and desktop are set in <a href="#/settings">Settings</a>.</p>
   ${feed.length ? `<table>${feed.slice().reverse().map(n => `<tr><td class="muted">${ago(n.at)}</td><td><b>${esc(n.title)}</b>${n.message ? `<div class="muted">${esc(n.message)}</div>` : ""}${(n.errors || []).map(e => `<div class="fail">${esc(e)}</div>`).join("")}</td><td>${n.task ? `<a href="#/tasks/${enc(n.repo)}/${enc(n.task)}" class="close">open</a>` : ""}</td></tr>`).join("")}</table>` : `<p class="muted">Nothing yet.</p>`}
   <div class="actions"><button class="btn" id="dlgClose">Close</button></div>`, b => {
    $("#dlgClose", b).onclick = closeDialog; $$(".close", b).forEach(a => a.onclick = closeDialog);
    const p = $("#perm", b); if (p) p.onclick = async () => { await Notification.requestPermission(); closeDialog() };
  });
};

// - Session -
function showLogin(msg) {
  if (es) { es.close(); es = null }
  $("#nav").innerHTML = ""; $("#newBtn").hidden = true; $("#ctl").textContent = "";
  $("#view").innerHTML = `<form class="login" id="loginF"><h2>Sign in to Spine</h2><p class="hint">Paste the token from <code>spine users add NAME</code>.</p>
   <input type="password" name="token" autocomplete="current-password" placeholder="spn_…" required><div class="err" id="loginErr">${esc(msg || "")}</div>
   <button class="btn primary" type="submit">Sign in</button></form>`;
  $("#loginF").onsubmit = async e => {
    e.preventDefault();
    try { await send("POST", "/api/login", { token: e.target.token.value }); location.reload() } catch (x) { $("#loginErr").textContent = x.message }
  };
}
async function boot() {
  me = await fetch("/api/me").then(r => r.json()).catch(() => ({}));
  if (me.auth && !me.signed_in) { showLogin(); return }
  route = parseHash(); renderNav(); connect();
}
function connect() {
  es = new EventSource("/api/stream");
  es.addEventListener("overview", e => { ov = JSON.parse(e.data); renderHeader(); renderNav(); pullNotifications(); onOverview() });
  es.onerror = () => { $("#ctl").textContent = "disconnected; retrying…" };
}
let first = true;
function onOverview() { if (first) { first = false; render(); return } if (route.view === "tasks" || route.view === "graph" && !graphBusy) refresh() }

// - Views -
let liveES = null;
function render() {
  if (liveES && !(route.view === "tasks" && route.tab === "live")) { liveES.close(); liveES = null }
  renderNav();
  const v = route.view;
  ({ tasks: viewTasks, new: viewNew, plan: viewPlan, graph: viewGraph, dash: viewDash, cost: viewCost, quality: viewQuality,
    slots: viewSlots, health: viewHealth, audit: viewAudit, settings: viewSettings }[v] || viewTasks)();
}
function refresh() { if (route.view === "tasks") { renderList(); if (route.id && route.tab === "overview" && !editing) loadDetail(true) } else if (route.view === "graph") viewGraph() }

// ===== Tasks =====
let filter = "active", search = { q: "", phase: "", by: "", from: "", to: "" }, showSearch = false, picked = new Set(), editing = false;
try { filter = localStorage.getItem("spine.filter") || "active" } catch (e) { }
const FILTERS = { active: t => !DONE.includes(t.phase), needs: t => t.needs_you, done: t => t.phase === "Integrated", plans: t => t.kind === "plan", all: () => true };
const FLABEL = { active: "Active", needs: "Needs you", done: "Integrated", plans: "Plans", all: "All" };
const inRepo = () => ov.tasks.filter(t => repo === "all" || t.repo === repo);
function matches(t) {
  const q = search.q.toLowerCase();
  if (q && !(t.id + " " + t.title + " " + (t.commit || "") + " " + (t.message || "")).toLowerCase().includes(q)) return false;
  if (search.phase && t.phase !== search.phase) return false;
  if (search.by && !(t.by || "").toLowerCase().includes(search.by.toLowerCase())) return false;
  if (search.from && t.created < search.from) return false;
  if (search.to && t.created.slice(0, 10) > search.to) return false;
  return true;
}
function viewTasks() {
  if (!$("#list")) {
    $("#view").innerHTML = `<div class="split"><div id="list"><div class="filters" id="filters"></div><div id="searchBox"></div><div id="bulk"></div><div id="tasks"></div></div><div id="detail" class="page"></div></div>`;
  }
  renderList(); loadDetail();
}
function renderList() {
  if (!$("#list")) return;
  const ts = inRepo();
  $("#filters").innerHTML = Object.keys(FILTERS).map(k => `<button data-f="${k}" class="chip ${k === filter ? "on" : ""}">${FLABEL[k]} ${ts.filter(FILTERS[k]).length}</button>`).join("") +
    `<button class="chip ${showSearch ? "on" : ""}" id="toggleSearch" title="Search and filter history">🔍 Search</button>`;
  $$("#filters [data-f]").forEach(b => b.onclick = () => { filter = b.dataset.f; try { localStorage.setItem("spine.filter", filter) } catch (e) { } renderList() });
  $("#toggleSearch").onclick = () => { showSearch = !showSearch; if (!showSearch) search = { q: "", phase: "", by: "", from: "", to: "" }; renderList() };
  if (showSearch && !$("#sq")) {
    $("#searchBox").innerHTML = `<div class="search"><input id="sq" placeholder="Text: id, title, commit, message" value="${esc(search.q)}">
     <div class="row"><select id="sphase"><option value="">any phase</option>${["Pending", "Provisioned", "Working", "Verifying", "Verified", "Integrated", "Escalated", "Failed", "Cancelled", "Planned"].map(p => `<option ${p === search.phase ? "selected" : ""}>${p}</option>`).join("")}</select>
     <input id="sby" placeholder="author" value="${esc(search.by)}"></div>
     <div class="row"><input type="date" id="sfrom" value="${esc(search.from)}" title="created from"><input type="date" id="sto" value="${esc(search.to)}" title="created to"></div></div>`;
    const upd = () => { search = { q: $("#sq").value, phase: $("#sphase").value, by: $("#sby").value, from: $("#sfrom").value, to: $("#sto").value }; renderRows() };
    ["#sq", "#sby"].forEach(s => $(s).oninput = upd); ["#sphase", "#sfrom", "#sto"].forEach(s => $(s).onchange = upd);
  } else if (!showSearch) $("#searchBox").innerHTML = "";
  renderRows();
}
function renderRows() {
  const rows = inRepo().filter(showSearch ? () => true : FILTERS[filter]).filter(matches);
  const showRepo = ov.multi && repo === "all";
  const sel = route.repo && route.id ? route.repo + "/" + route.id : null;
  for (const k of [...picked]) if (!ov.tasks.some(t => taskKey(t) === k)) picked.delete(k);
  $("#tasks").innerHTML = rows.length ? rows.map(t => {
    const k = taskKey(t), web = ov.repos.find(r => r.name === t.repo)?.web_url;
    return `<div class="task ${k === sel ? "sel" : ""}" data-k="${esc(k)}">${can("operator") ? `<input type="checkbox" class="pick" data-k="${esc(k)}" ${picked.has(k) ? "checked" : ""} aria-label="select">` : "<span></span>"}
    <div><div class="t">${t.kind === "plan" ? `<span class="kindtag">plan</span> ` : ""}${esc(t.title)}</div>
    <div class="m">${showRepo ? `<span class="repo">${esc(t.repo)}</span>` : ""}<span class="badge b-${esc(t.phase)}">${esc(t.phase)}</span><span>${esc(t.reason)}</span>
    <span>a${t.attempt} r${t.round}</span>${t.tokens ? `<span>${fmtN(t.tokens)} tok</span>` : ""}<span>${ago(t.created)}</span>${t.by ? `<span>${esc(t.by)}</span>` : ""}
    ${t.commit ? (web ? `<a href="${esc(web)}/commit/${esc(t.commit)}" target="_blank" rel="noopener" class="mono">${esc(t.commit.slice(0, 8))}</a>` : `<span class="mono">${esc(t.commit.slice(0, 8))}</span>`) : ""}</div></div></div>`
  }).join("") : `<div class="empty">No tasks${showSearch ? " match" : ""}.</div>`;
  $$("#tasks .task").forEach(e => e.onclick = ev => { if (ev.target.classList.contains("pick") || ev.target.tagName === "A") return; editing = false; const [r, ...id] = e.dataset.k.split("/"); go(`tasks/${r}/${id.join("/")}`) });
  $$("#tasks .pick").forEach(c => c.onchange = () => { c.checked ? picked.add(c.dataset.k) : picked.delete(c.dataset.k); renderBulk() });
  renderBulk();
}
function renderBulk() {
  const n = picked.size;
  if (!n) { $("#bulk").innerHTML = ""; return }
  $("#bulk").innerHTML = `<div class="bulkbar"><b>${n} selected</b><button class="btn small" data-b="approve">Approve</button><button class="btn small" data-b="retry">Retry</button>
   <button class="btn small" data-b="hint">Hint…</button><button class="btn small danger" data-b="cancel">Cancel</button><button class="btn small" id="bclear">Clear</button></div>`;
  $("#bclear").onclick = () => { picked.clear(); renderRows() };
  $$("#bulk [data-b]").forEach(b => b.onclick = async () => {
    const action = b.dataset.b; let text = "";
    if (action === "hint") { text = prompt(`Hint for the next round of ${n} task(s):`) || ""; if (!text.trim()) return }
    if (action === "cancel" && !confirm(`Cancel ${n} task(s)?`)) return;
    const tasks = [...picked].map(k => { const i = k.indexOf("/"); return { repo: k.slice(0, i), id: k.slice(i + 1) } });
    try {
      const r = await send("POST", "/api/bulk", { action, text, tasks });
      const bad = r.results.filter(x => !x.queued);
      toast(`${action}: ${r.queued} queued${bad.length ? `, ${bad.length} skipped<div class="muted">${bad.slice(0, 5).map(x => esc(x.id + ": " + x.error)).join("<br>")}</div>` : ""}`, bad.length ? "warn" : "ok");
      picked.clear(); renderRows();
    } catch (e) { toast(esc(e.message), "bad") }
  });
}

// - Task detail -
let detailSeq = 0;
async function loadDetail(soft) {
  const d = $("#detail"); if (!d) return;
  if (!route.id) { d.innerHTML = `<div class="empty">Select a task${can("operator") ? `, or <a href="#/new">create one</a>` : ""}.</div>`; return }
  if (editing && soft) return;
  const my = ++detailSeq, r = route.repo, id = route.id, tab = route.tab || "overview";
  let t;
  try { t = await get(taskURL(r, id)) } catch (e) {
    if (my === detailSeq) d.innerHTML = `<h2>${esc(id)}</h2><p class="hint">${esc(pendingNote[r + "/" + id] || e.message)}</p>`; return
  }
  if (my !== detailSeq) return;
  delete pendingNote[r + "/" + id];
  const s = t.status, sp = t.spec;
  const tabs = [["overview", "Overview"], ["changes", "Changes"], ["rounds", "Rounds"], ["logs", "Logs"], ["live", s.phase === "Working" || s.phase === "Verifying" ? "● Live" : "Live"]];
  if (sp.kind === "plan" && s.plan) tabs.splice(1, 0, ["planreview", "Plan"]);
  if (soft && tab !== "overview") return;
  const acts = [];
  if (can("operator")) {
    if (s.phase === "Escalated" && s.reason === "AwaitingApproval") acts.push(`<button class="btn primary" data-a="approve">Review & approve</button>`);
    if (sp.kind === "plan" && s.phase === "Escalated" && s.plan) acts.push(`<button class="btn primary" data-a="planreview">Review plan</button>`);
    if (STOPPED.includes(s.phase)) acts.push(`<button class="btn" data-a="retry">Retry</button>`);
    if (!["Integrated", "Planned"].includes(s.phase)) acts.push(`<button class="btn" data-a="hint">Add hint…</button>`);
    if (s.phase === "Pending" || STOPPED.includes(s.phase)) acts.push(`<button class="btn" data-a="edit">${s.phase === "Pending" ? "Edit" : "Edit & retry"}</button>`);
    if (!DONE.includes(s.phase)) acts.push(`<button class="btn danger" data-a="cancel">Cancel</button>`);
  }
  d.innerHTML = `<h2>${sp.kind === "plan" ? `<span class="kindtag">plan</span> ` : ""}${esc(sp.title || sp.intent.split("\n")[0])}</h2>
   <span class="badge b-${esc(s.phase)}">${esc(s.phase)}</span> <span class="mono">${esc(s.reason || "")}</span> ${s.message ? `<span class="muted">— ${esc(s.message)}</span>` : ""}
   <div class="actions">${acts.join("")}</div>
   <div class="tabs">${tabs.map(([k, l]) => `<a href="#/tasks/${enc(r)}/${enc(id)}/${k}" class="${k === tab ? "on" : ""}">${l}</a>`).join("")}</div><div id="tab"></div>`;
  $$("#detail .actions button").forEach(b => b.onclick = () => taskAction(b.dataset.a, r, t));
  const fn = { overview: tabOverview, changes: tabChanges, rounds: tabRounds, logs: tabLogs, live: tabLive, planreview: tabPlanReview }[tab] || tabOverview;
  fn(r, t);
}
const pendingNote = {};
async function command(r, id, action, body) {
  try {
    const j = await send("POST", taskURL(r, id, "/" + action), body);
    toast(`${esc(action)} queued${j.controller ? "" : " — it applies when this repository's controller runs"}`, "ok");
    setTimeout(() => loadDetail(), 800); return true;
  } catch (e) { toast(esc(e.message), "bad"); return false }
}
function taskAction(a, r, t) {
  const id = t.id;
  if (a === "approve") { go(`tasks/${r}/${id}/changes`); setTimeout(() => toast("Review the change, then press <b>Approve and integrate</b> above the diff."), 300); return }
  if (a === "planreview") { go(`tasks/${r}/${id}/planreview`); return }
  if (a === "edit") { editTask(r, t); return }
  if (a === "cancel") { if (confirm(`Cancel ${id}? Its worktree is released.`)) command(r, id, "cancel"); return }
  if (a === "retry") {
    dialog(`<h2>Retry ${esc(id)}</h2><p class="hint">A fresh attempt starts from the current base. Optionally tell the agent what to do differently.</p>
     <form class="f" id="rf"><label>Hint (optional)<textarea name="text" maxlength="2000" placeholder="The bug is in parseHeader, not the handler."></textarea></label>
     <div class="actions"><button class="btn primary">Retry</button><button type="button" class="btn" id="dc">Cancel</button></div></form>`, b => {
      $("#dc", b).onclick = closeDialog; $("#rf", b).onsubmit = async e => { e.preventDefault(); if (await command(r, id, "retry", { text: e.target.text.value })) closeDialog() };
    }); return;
  }
  if (a === "hint") {
    const resumes = t.status.phase === "Escalated";
    dialog(`<h2>Hint for ${esc(id)}</h2><p class="hint">${resumes ? "The task is escalated: the hint resumes it with another repair round in its kept worktree (or a fresh attempt if the worktree is gone)." : STOPPED.includes(t.status.phase) ? "The task is stopped: the hint starts a fresh attempt." : "The next repair round receives it alongside the gate feedback."}</p>
     <form class="f" id="hf"><label>Hint<textarea name="text" required maxlength="2000" placeholder="The failing test needs the fixture in testdata/; don't change the API."></textarea></label>
     <div class="actions"><button class="btn primary">${resumes ? "Send and resume" : "Send hint"}</button><button type="button" class="btn" id="dc">Cancel</button></div></form>`, b => {
      $("#dc", b).onclick = closeDialog; $("#hf", b).onsubmit = async e => { e.preventDefault(); if (await command(r, id, "hint", { text: e.target.text.value })) closeDialog() };
    });
  }
}
function describe(e) {
  const d = e.data || {};
  switch (e.kind) {
    case "transition": return `${esc(d.from)} → <b>${esc(d.to)}</b> <span class="mono">${esc(d.reason)}</span> ${esc(d.message || "")}`;
    case "agent_start": return `agent <b>${esc(d.adapter)}</b> started (attempt ${d.attempt}, round ${d.round})` + (d.feedback?.length ? ` with ${d.feedback.length} feedback item(s)` : "");
    case "agent": return d.kind === "tool_call" ? `· ${esc(d.title)}` : d.kind === "permission" ? `· permission <b>${esc(d.status)}</b>: ${esc(d.title)}` : d.kind === "guard" ? `· <span class="warn">guard ${esc(d.title)}</span>: ${esc(d.detail)}` : null;
    case "queued": return `<span class="mono">${esc(d.reason)}</span>`;
    case "outcome": return `agent finished: <b>${esc(d.reason)}</b> ${esc(d.stop_reason || "")} (${fmtN(d.tokens_used)} tokens${d.cost_usd ? ", " + fmtUSD(d.cost_usd) : ""})`;
    case "verdict": { const v = d.verdict || {}; let s = v.passed ? `<span class="pass">gates passed</span>` : `<span class="fail">gate <b>${esc(v.failed_gate)}</b> failed</span>`; s += d.on_merge ? " on the merge result" : " on the branch"; return s }
    case "decision": return `decision: <b>${esc(d.decision?.action)}</b> (${esc(d.decision?.reason)})${d.supervisor ? ` <span class="mono">by ${esc(d.supervisor)}</span>` : ""}`;
    case "integrated": return `<span class="pass">integrated</span> ${esc(d.base_ref)} at <span class="mono">${esc(String(d.commit).slice(0, 12))}</span>`;
    case "submitted": return "submitted"; case "spec": return `spec edited (generation ${d.generation})`;
    case "command": return `<b>${esc(d.kind)}</b> by ${esc(d.by)}${d.text ? `: <i>${esc(d.text)}</i>` : ""}${d.seq ? ` (snapshot #${d.seq})` : ""}`;
    case "command_rejected": return `<span class="fail">command ${esc(d.command?.kind)} rejected</span>: ${esc(d.error)}`;
    case "plan": return `plan proposed: ${d.tasks} tasks`; case "plan_rejected": return `<span class="warn">plan rejected by spec.validate</span>: ${esc((d.problems || []).join("; ")).slice(0, 300)}`;
    case "resumed_from_snapshot": return `resumed from snapshot #${d.seq} (${esc(d.label)})`;
    default: return e.kind.endsWith("error") ? `<span class="fail">${esc(e.kind)}</span>: ${esc(d.error)}` : null;
  }
}
function gateTable(g, r) {
  return `<table><tr><th>Gate</th><th>Tool</th><th>Result</th><th class="num">New</th><th class="num">Excused</th><th class="num">Fixed</th><th>Logs</th></tr>` +
    (g.gates || []).map(x => `<tr><td>${esc(x.gate)}</td><td class="mono">${esc(x.tool)}</td><td class="${x.skipped ? "skip" : x.exit_code === 0 ? "pass" : "fail"}">${x.skipped ? "skipped" : x.exit_code === 0 ? "pass" : "fail " + esc(x.reason || "")}${x.cached ? ' <span class="muted">(cached)</span>' : ""}</td>
     <td class="num">${(x.new || []).length}</td><td class="num">${x.excused || 0}</td><td class="num">${(x.fixed || []).length}</td><td>${artLinks(r, x.artifacts)}</td></tr>
     ${(x.new || []).slice(0, 8).map(dg => `<tr><td></td><td colspan="6" class="diag"><span class="mono">${esc(dg.code)}</span> ${esc(dg.file || "")}${dg.line ? ":" + dg.line : ""} ${esc((dg.message || "").split("\n")[0]).slice(0, 200)}</td></tr>`).join("")}`).join("") + `</table>`;
}
function artLinks(r, arts) {
  return (arts || []).map(a => `<a href="/api/repos/${enc(r)}/artifacts/${enc(a.digest)}" target="_blank" rel="noopener" title="${esc(a.digest)}">${esc(a.name)}</a> <span class="muted">${fmtBytes(a.size)}</span>`).join("<br>") || `<span class="muted">—</span>`;
}
async function tabOverview(r, t) {
  const s = t.status, sp = t.spec, b = sp.budget || {};
  const evs = await get(taskURL(r, t.id, "/events")).catch(() => []);
  const conds = (s.conditions || []).map(c => `<span class="${c.status ? "pass" : "fail"}">${esc(c.type)}</span>`).join(" · ");
  const web = ov.repos.find(x => x.name === r)?.web_url;
  const gates = s.last_gate ? `<section class="card"><h3>Last gate run${s.last_gate.on_merge ? " (merge result)" : ""}</h3>${gateTable(s.last_gate, r)}</section>` : "";
  const hints = (s.hints || []).length ? `<section class="card"><h3>Hints for the next round</h3>${s.hints.map(h => `<div>“${esc(h.text)}” <span class="muted">— ${esc(h.by)}, ${ago(h.at)} ago</span></div>`).join("")}</section>` : "";
  $("#tab").innerHTML = `<dl class="kv">${ov.multi ? `<dt>Repository</dt><dd>${esc(r)}</dd>` : ""}<dt>ID</dt><dd class="mono">${esc(t.id)}</dd>
   <dt>Base</dt><dd class="mono">${esc(sp.base_ref)}</dd><dt>Scope</dt><dd class="mono">${esc((sp.scope || ["everything"]).join(", "))}</dd>
   ${(sp.depends_on || []).length ? `<dt>After</dt><dd class="mono">${sp.depends_on.map(esc).join(", ")}</dd>` : ""}
   <dt>Agent</dt><dd>${esc(sp.adapter || "default")}</dd>
   <dt>Attempt</dt><dd>${s.attempt} of ${b.max_attempts || 2}, round ${s.patch_round} of ${b.max_patch_rounds || 3}</dd>
   <dt>Tokens</dt><dd>${fmtN(s.tokens_used)}${b.max_tokens ? ` of ${fmtN(b.max_tokens)}` : ""}</dd>
   ${(sp.acceptance || []).length ? `<dt>Gates</dt><dd>${sp.acceptance.map(g => esc(g.name)).join(", ")} <span class="muted">(chosen for this task)</span></dd>` : ""}
   ${sp.created_by ? `<dt>Created by</dt><dd>${esc(sp.created_by)}${sp.template ? ` from template <i>${esc(sp.template)}</i>` : ""}, ${ago(t.created_at)} ago</dd>` : ""}
   ${s.worktree && !DONE.includes(s.phase) ? `<dt>Worktree</dt><dd class="mono">${esc(s.worktree.path)}</dd>` : ""}
   ${s.integrated_commit ? `<dt>Commit</dt><dd class="mono">${web ? `<a href="${esc(web)}/commit/${esc(s.integrated_commit)}" target="_blank" rel="noopener">${esc(s.integrated_commit)}</a>` : esc(s.integrated_commit)} · <a href="#/tasks/${enc(r)}/${enc(t.id)}/changes">view diff</a></dd>` : ""}
   <dt>Conditions</dt><dd>${conds || "—"}</dd></dl>
   ${hints}<section class="card"><h3>Intent</h3><pre style="font-family:inherit;font-size:14px">${esc(sp.intent)}</pre></section>${gates}
   ${s.last_outcome?.message ? `<section class="card"><h3>Agent's final message (round ${s.last_outcome.round})</h3><pre>${esc(s.last_outcome.message)}</pre></section>` : ""}
   <section class="card"><h3>Timeline</h3>${evs.map(e => { const h = describe(e); return h ? `<div class="ev"><time>${tm(e.at)}</time><div>${h}</div></div>` : "" }).join("")}</section>`;
}

// - Changes (diff viewer) -
async function tabChanges(r, t) {
  const s = t.status, snaps = s.snapshots || [];
  const merges = [];
  for (const g of [...(s.gate_history || [])]) if (g.on_merge && g.commit && !merges.includes(g.commit)) merges.push(g.commit);
  if (s.proposed_commit && !merges.includes(s.proposed_commit) && s.phase === "Escalated") merges.push(s.proposed_commit);
  if (s.integrated_commit && !merges.includes(s.integrated_commit)) merges.push(s.integrated_commit);
  if (!snaps.length && !merges.length) { $("#tab").innerHTML = `<div class="empty">No snapshot yet: the agent has not finished a round.</div>`; return }
  const opts = snaps.map(x => `<option value="seq:${x.seq}">#${x.seq} ${esc(x.label)}${s.best_snapshot?.seq === x.seq ? " (best)" : ""}</option>`).reverse()
    .concat(merges.map(c => `<option value="merge:${c}">${c === s.integrated_commit ? "integrated commit" : c === s.proposed_commit ? "what would land (merge result)" : "merge result"} ${c.slice(0, 10)}</option>`).reverse());
  const approval = s.phase === "Escalated" && s.reason === "AwaitingApproval" && can("operator");
  $("#tab").innerHTML = `${approval ? `<section class="card" style="border-color:var(--warn)"><h3>Approval</h3><p>The merge result passed every gate and waits for you. Below is exactly what would land on <code>${esc(t.spec.base_ref.replace("refs/heads/", ""))}</code>; review it, then:</p>
     <div class="actions"><button class="btn primary" id="approveBtn">Approve and integrate</button><button class="btn" id="hintBtn">Request changes (hint)…</button></div></section>` : ""}
   <div class="actions"><label class="hint">Show <select id="dsel">${opts.join("")}</select></label>
   <label class="hint">against <select id="dfrom"><option value="">its base</option>${snaps.map(x => `<option value="${x.seq}">#${x.seq} ${esc(x.label)}</option>`).reverse().join("")}</select></label>
   <label class="hint check"><input type="checkbox" id="dsplit"> collapse all</label></div><div id="diffOut"><div class="muted">Loading…</div></div>`;
  if (approval) {
    $("#approveBtn").onclick = async () => { if (confirm("Integrate this change into " + t.spec.base_ref.replace("refs/heads/", "") + "?")) await command(r, t.id, "approve") };
    $("#hintBtn").onclick = () => taskAction("hint", r, t);
    if (s.proposed_commit) $("#dsel").value = "merge:" + s.proposed_commit;
    else if (merges.length) $("#dsel").value = "merge:" + merges[merges.length - 1];
  }
  const loadD = async () => {
    const [k, v] = $("#dsel").value.split(":"); const from = $("#dfrom").value;
    $("#dfrom").disabled = k === "merge";
    const q = k === "merge" ? `?merge=${v}` : `?seq=${v}${from && from !== v ? "&from=" + from : ""}`;
    $("#diffOut").innerHTML = `<div class="muted">Loading…</div>`;
    try { renderDiff(await get(taskURL(r, t.id, "/diff" + q)), $("#dsplit").checked) } catch (e) { $("#diffOut").innerHTML = `<p class="fail">${esc(e.message)}</p>` }
  };
  $("#dsel").onchange = loadD; $("#dfrom").onchange = loadD; $("#dsplit").onchange = loadD;
  if (route.from) { $("#dfrom").value = route.from; route.from = null }
  if (route.seq) { $("#dsel").value = "seq:" + route.seq; route.seq = null }
  loadD();
}
function renderDiff(d, collapsed) {
  const adds = d.files.reduce((a, f) => a + f.adds, 0), dels = d.files.reduce((a, f) => a + f.dels, 0);
  const parts = splitPatch(d.patch);
  let h = `<p class="hint">${esc(d.head_label)} against ${esc(d.base_label)}: ${d.files.length} file(s), <span class="plus">+${adds}</span> <span class="minus">−${dels}</span>${d.truncated ? ' <span class="warn">(diff truncated at 4 MiB)</span>' : ""}</p>`;
  if (!d.files.length) { $("#diffOut").innerHTML = h + `<div class="empty">No changes.</div>`; return }
  h += `<table class="files">${d.files.map((f, i) => `<tr><td><span class="badge">${esc(f.status)}</span></td><td><a href="#" data-f="${i}" class="mono">${esc(f.old_path ? f.old_path + " → " : "")}${esc(f.path)}</a></td><td class="num plus">+${f.adds}</td><td class="num minus">−${f.dels}</td></tr>`).join("")}</table>`;
  h += d.files.map((f, i) => {
    const p = parts[f.path] || parts[f.old_path] || "";
    return `<details class="dfile" id="df${i}" ${collapsed ? "" : "open"}><summary><span>${esc(f.path)}</span><span class="plus">+${f.adds}</span><span class="minus">−${f.dels}</span></summary>${f.binary ? `<p class="muted" style="padding:8px">binary file</p>` : hunkTable(p)}</details>`;
  }).join("");
  $("#diffOut").innerHTML = h;
  $$("#diffOut [data-f]").forEach(a => a.onclick = e => { e.preventDefault(); const el = $("#df" + a.dataset.f); el.open = true; el.scrollIntoView({ behavior: "smooth" }) });
}
function splitPatch(patch) {
  const out = {}; let cur = null, buf = [];
  const flush = () => { if (cur) out[cur] = buf.join("\n") };
  for (const ln of patch.split("\n")) {
    if (ln.startsWith("diff --git ")) { flush(); buf = []; const m = ln.match(/ b\/(.*)$/); cur = m ? m[1] : ln; continue }
    buf.push(ln);
  }
  flush(); return out;
}
function hunkTable(p) {
  let o = 0, n = 0; const rows = [];
  for (const ln of p.split("\n")) {
    if (ln.startsWith("@@")) { const m = ln.match(/@@ -(\d+)(?:,\d+)? \+(\d+)/); if (m) { o = +m[1]; n = +m[2] } rows.push(`<tr class="h"><td class="ln"></td><td class="ln"></td><td class="c">${esc(ln)}</td></tr>`); continue }
    if (ln.startsWith("+++") || ln.startsWith("---") || ln.startsWith("index ") || ln.startsWith("new file") || ln.startsWith("deleted file") || ln.startsWith("similarity") || ln.startsWith("rename ") || ln.startsWith("old mode") || ln.startsWith("new mode")) continue;
    if (ln.startsWith("+")) rows.push(`<tr class="a"><td class="ln"></td><td class="ln">${n++}</td><td class="c">${esc(ln)}</td></tr>`);
    else if (ln.startsWith("-")) rows.push(`<tr class="d"><td class="ln">${o++}</td><td class="ln"></td><td class="c">${esc(ln)}</td></tr>`);
    else if (ln.startsWith(" ")) rows.push(`<tr><td class="ln">${o++}</td><td class="ln">${n++}</td><td class="c">${esc(ln)}</td></tr>`);
    else if (ln.startsWith("\\")) rows.push(`<tr><td class="ln"></td><td class="ln"></td><td class="c muted">${esc(ln)}</td></tr>`);
  }
  return `<table>${rows.join("")}</table>`;
}

// - Rounds -
async function tabRounds(r, t) {
  $("#tab").innerHTML = `<div class="muted">Loading…</div>`;
  const d = await get(taskURL(r, t.id, "/rounds")).catch(e => ({ error: e.message }));
  if (d.error) { $("#tab").innerHTML = `<p class="fail">${esc(d.error)}</p>`; return }
  if (!d.rounds.length) { $("#tab").innerHTML = `<div class="empty">No rounds yet.</div>`; return }
  const canRoll = can("operator") && STOPPED.includes(t.status.phase);
  const dg = x => `<div class="diag"><span class="mono">${esc(x.code)}</span> ${esc(x.file || "")}${x.line ? ":" + x.line : ""} ${esc((x.message || "").split("\n")[0]).slice(0, 220)}</div>`;
  let prevSnap = null;
  $("#tab").innerHTML = `<p class="hint">Each repair round: what the agent was told, how its run ended, what the gates said, and what changed since the previous round.${canRoll ? " You can continue the task from any snapshot." : ""}</p>` + d.rounds.map(x => {
    const o = x.outcome, g = x.gate, snap = x.snapshots[x.snapshots.length - 1];
    const cmp = snap && prevSnap && prevSnap.attempt === snap.attempt ? `<a href="#" data-cmp="${snap.seq}:${prevSnap.seq}">diff vs previous round</a>` : "";
    if (snap) prevSnap = snap;
    return `<div class="round"><h4>Attempt ${x.attempt} · round ${x.round} <span class="pill">${esc(x.adapter || "agent")}</span>
      ${o ? `<span class="badge ${o.reason === "Completed" ? "b-Integrated" : "b-Failed"}">${esc(o.reason)}</span>` : `<span class="badge">running</span>`}
      ${g ? (g.passed ? `<span class="pass">gates passed</span>` : `<span class="fail">${esc(g.failed_gate)} failed</span>`) : ""}
      ${x.merge_gate ? (x.merge_gate.passed ? `<span class="pass">merge result passed</span>` : `<span class="fail">merge result: ${esc(x.merge_gate.failed_gate)} failed</span>`) : ""}
      <span class="muted">${x.started ? ago(x.started) + " ago" : ""}</span></h4>
     ${x.feedback.length ? `<details><summary>Feedback it received (${x.feedback.length})</summary>${x.feedback.map(f => `<div><b>${esc(f.source)}</b>${f.hint ? ": " + esc(f.hint) : ""}${(f.conflicts || []).map(c => `<div class="diag">conflict: ${esc(c)}</div>`).join("")}${(f.diagnostics || []).slice(0, 10).map(dg).join("")}</div>`).join("")}</details>` : `<div class="muted">No feedback (first round).</div>`}
     ${o ? `<div>${fmtN(o.tokens_used)} tokens${o.cost_usd ? " · " + fmtUSD(o.cost_usd) : ""}${o.turns ? " · " + o.turns + " turns" : ""} · ${fmtMin(o.duration_ns / 6e10)} · ${x.tools.length} tool calls${x.tools.filter(z => z.status === "failed").length ? ` (${x.tools.filter(z => z.status === "failed").length} failed)` : ""}</div>` : ""}
     ${(x.guards || []).map(gd => `<div class="warn">guard ${esc(gd.title)}: ${esc(gd.detail || gd.status || "")}</div>`).join("")}
     ${o?.message ? `<details><summary>Agent's final message</summary><pre>${esc(o.message)}</pre></details>` : ""}
     ${x.tools.length ? `<details><summary>Tool calls</summary>${x.tools.map(z => `<div class="mono ${z.status === "failed" ? "fail" : ""}">${esc(z.title)}</div>`).join("")}</details>` : ""}
     ${g ? `<details ${g.passed ? "" : "open"}><summary>Gates</summary>${gateTable(g, r)}</details>` : ""}
     ${x.merge_gate ? `<details><summary>Gates on the merge result</summary>${gateTable(x.merge_gate, r)}</details>` : ""}
     ${g ? `<div style="margin-top:6px">${x.fixed.length ? `<div class="pass">Fixed ${x.fixed.length}:</div>${x.fixed.slice(0, 8).map(dg).join("")}` : ""}
       ${x.introduced.length ? `<div class="fail">${x.round ? "Introduced" : "Failing"} ${x.introduced.length}:</div>${x.introduced.slice(0, 8).map(dg).join("")}` : ""}
       ${x.persisting.length ? `<div class="warn">Still failing ${x.persisting.length}:</div>${x.persisting.slice(0, 5).map(dg).join("")}` : ""}</div>` : ""}
     ${x.decision ? `<div style="margin-top:6px">Decision: <b>${esc(x.decision.decision?.action)}</b> — ${esc(x.decision.decision?.reason)} <span class="muted">by ${esc(x.decision.supervisor || "rules")}</span>${x.decision.decision?.hint ? `<div class="diag">hint: ${esc(x.decision.decision.hint)}</div>` : ""}</div>` : ""}
     <div class="actions">${x.snapshots.map(sn => `<a href="#" data-snap="${sn.seq}">snapshot #${sn.seq} diff</a>${canRoll ? ` <button class="btn small" data-roll="${sn.seq}">Continue from #${sn.seq}</button>` : ""}`).join(" ")} ${cmp}</div></div>`;
  }).join("");
  $$("[data-snap]").forEach(a => a.onclick = e => { e.preventDefault(); route.seq = a.dataset.snap; go(`tasks/${r}/${t.id}/changes`) });
  $$("[data-cmp]").forEach(a => a.onclick = e => { e.preventDefault(); const [h, f] = a.dataset.cmp.split(":"); route.seq = h; route.from = f; go(`tasks/${r}/${t.id}/changes`) });
  $$("[data-roll]").forEach(b => b.onclick = async () => {
    const seq = +b.dataset.roll;
    if (!confirm(`Continue ${t.id} from snapshot #${seq}? ${t.status.phase === "Escalated" ? "The kept worktree is reset to it and another round starts." : "A fresh attempt starts from the current base with that snapshot's change applied."}`)) return;
    await command(r, t.id, "rollback", { seq });
  });
}

// - Logs -
async function tabLogs(r, t) {
  const d = await get(taskURL(r, t.id, "/rounds")).catch(() => ({ rounds: [] }));
  const rows = [];
  for (const x of d.rounds) for (const g of [x.gate, x.merge_gate].filter(Boolean)) for (const v of g.gates || []) for (const a of v.artifacts || [])
    rows.push({ round: `a${x.attempt} r${x.round}${g.on_merge ? " (merge)" : ""}`, gate: v.gate, tool: v.tool, pass: v.exit_code === 0 && !v.skipped, a });
  const msgs = d.rounds.filter(x => x.outcome?.message);
  $("#tab").innerHTML = `<section class="card"><h3>Gate output</h3>${rows.length ? `<table><tr><th>Round</th><th>Gate</th><th>Tool</th><th>Result</th><th>Log</th><th></th></tr>${rows.map((x, i) => `<tr><td>${esc(x.round)}</td><td>${esc(x.gate)}</td><td class="mono">${esc(x.tool)}</td><td class="${x.pass ? "pass" : "fail"}">${x.pass ? "pass" : "fail"}</td>
     <td><a href="#" data-view="${i}">${esc(x.a.name)}</a> <span class="muted">${fmtBytes(x.a.size)}</span></td><td><a href="/api/repos/${enc(r)}/artifacts/${enc(x.a.digest)}" target="_blank" rel="noopener">open</a> · <a href="/api/repos/${enc(r)}/artifacts/${enc(x.a.digest)}?download=1">download</a></td></tr>`).join("")}</table>`
      : `<p class="muted">No stored logs. Only command-running gates (go.test, cmd.run, …) keep full output; cached results point at the log of the run that filled the cache.</p>`}
     <div id="logView"></div></section>
   <section class="card"><h3>Agent final messages</h3>${msgs.length ? msgs.map(x => `<details><summary>Attempt ${x.attempt}, round ${x.round}: ${esc(x.outcome.reason)}</summary><pre>${esc(x.outcome.message)}</pre></details>`).join("") : `<p class="muted">None yet.</p>`}</section>`;
  $$("[data-view]").forEach(a => a.onclick = async e => {
    e.preventDefault(); const x = rows[+a.dataset.view];
    const txt = await fetch(`/api/repos/${enc(r)}/artifacts/${enc(x.a.digest)}?tail=200000`).then(r => r.text()).catch(e => e.message);
    $("#logView").innerHTML = `<h3 class="sec">${esc(x.gate)} · ${esc(x.round)}${x.a.size > 200000 ? " (last 200 KB)" : ""}</h3><pre class="log">${txt ? esc(txt) : '<span class="muted">(the tool printed nothing)</span>'}</pre>`;
  });
}

// - Live -
function tabLive(r, t) {
  if (liveES) { liveES.close(); liveES = null }
  $("#tab").innerHTML = `<p class="hint" id="liveHead"></p><div id="liveFeed"></div><label class="hint check"><input type="checkbox" id="follow" checked> follow</label>`;
  const feed = $("#liveFeed"); let lastMsg = null;
  const head = s => $("#liveHead").innerHTML = (["Working", "Verifying", "Provisioned"].includes(s.phase) ? `<span class="dot"></span>` : "") +
    `<b>${esc(s.phase)}</b> ${esc(s.reason || "")} · attempt ${s.attempt}, round ${s.patch_round} · ${fmtN(s.tokens_used)} tokens`;
  head(t.status);
  liveES = new EventSource(taskURL(r, t.id, "/live"));
  liveES.addEventListener("task", e => head(JSON.parse(e.data).status));
  liveES.addEventListener("ev", e => {
    const ev = JSON.parse(e.data), d = ev.data || {};
    let el = null;
    if (ev.kind === "agent" && d.kind === "message") {
      if (lastMsg) { lastMsg.textContent += d.detail; } else { el = document.createElement("div"); el.className = "lv msg"; el.textContent = d.detail; lastMsg = el }
    } else {
      if (ev.kind === "agent" && d.kind === "usage") return;
      const h = describe(ev); if (!h) return;
      lastMsg = null; el = document.createElement("div"); el.className = "lv"; el.innerHTML = `<span class="k">${tm(ev.at)}</span>${h}`;
    }
    if (el) feed.append(el);
    if ($("#follow")?.checked) feed.scrollTop = feed.scrollHeight;
  });
}

// - Edit / new task form -
async function taskForm(el, opts) {
  // opts: {repo, task (api.Task) | null, onSubmit(repo, body)}
  const rs = ov.repos; const cur = opts.repo || curRepo();
  const t = opts.task, sp = t?.spec || {};
  el.innerHTML = `<form class="f" id="tf">${ov.multi && !t ? `<label>Repository<select name="repo">${rs.map(r => `<option ${r.name === cur ? "selected" : ""}>${esc(r.name)}</option>`).join("")}</select></label>` : `<input type="hidden" name="repo" value="${esc(cur)}">`}
   ${!t ? `<label>Template <span class="hint">(a saved task shape; fills the form)</span><select name="template"><option value="">none</option></select></label>` : ""}
   <label>What should the agent do?<textarea name="intent" required placeholder="Add input validation to the signup handler and cover it with tests.">${esc(sp.intent || "")}</textarea></label>
   <label>Title <span class="hint">(commit subject; default: first line of the intent)</span><input name="title" value="${esc(sp.title || "")}"></label>
   <div class="row"><label>Scope <span class="hint">(path globs, one per line; empty = whole repo)</span><textarea name="scope" style="min-height:64px" placeholder="src/**">${esc((sp.scope || []).join("\n"))}</textarea></label>
   <label>After <span class="hint">(task ids that must integrate first; repo:task for another repository)</span><textarea name="after" style="min-height:64px">${esc((sp.depends_on || []).join("\n"))}</textarea></label></div>
   <div class="row"><label>Base branch<input name="base" value="${esc((sp.base_ref || "").replace("refs/heads/", ""))}"></label><label>Agent<select name="agent"></select><span class="hint" id="agentHint"></span></label></div>
   <div class="row3"><label>Max attempts<input name="max_attempts" type="number" min="1" value="${sp.budget?.max_attempts || ""}"></label><label>Max repair rounds<input name="max_rounds" type="number" min="0" value="${sp.budget?.max_patch_rounds || ""}"></label>
   <label>Token budget<input name="max_tokens" type="number" min="0" step="1000" value="${sp.budget?.max_tokens || ""}" placeholder="unlimited"></label></div>
   <fieldset style="border:1px solid var(--line);border-radius:8px"><legend class="hint">Gates — what “done” means (<span id="gbase"></span>)</legend><div class="gatechecks" id="gchecks"><span class="muted">loading…</span></div>
    <p class="hint">Leave all checked to use the base's <code>.spine/gates.yaml</code> as it evolves; uncheck to pin a subset for this task.</p></fieldset>
   <label class="check"><input type="checkbox" name="require_approval" ${sp.escalation?.require_approval_before_merge ? "checked" : ""}> Hold for my approval before integrating</label>
   <div class="err" id="tfErr"></div><div class="actions"><button class="btn primary" type="submit">${t ? (t.status.phase === "Pending" ? "Save" : "Save and retry") : "Create task"}</button>
   ${!t ? `<button type="button" class="btn" id="saveTpl">Save as template…</button>` : ""}<button type="button" class="btn" id="tfCancel">Cancel</button></div></form>`;
  const f = $("#tf", el); let gatesAll = [], templates = [];
  const fill = async () => {
    const rn = f.repo.value;
    const [d, g, tp] = await Promise.all([get(`/api/repos/${enc(rn)}/defaults`).catch(e => ({ error: e.message })),
      get(`/api/repos/${enc(rn)}/gates${f.base.value ? "?base=" + enc(f.base.value) : ""}`).catch(e => ({ error: e.message })),
      t ? { templates: [] } : get(`/api/repos/${enc(rn)}/templates`).catch(() => ({ templates: [] }))]);
    f.base.placeholder = d.base || "main"; f.max_attempts.placeholder = d.max_attempts ?? ""; f.max_rounds.placeholder = d.max_rounds ?? "";
    const want = sp.adapter || d.agent;
    f.agent.innerHTML = (d.adapters || []).map(a => `<option value="${esc(a.name)}" ${a.name === want ? "selected" : ""}>${esc(a.label)}${a.name === d.agent ? " — default" : ""}</option>`).join("");
    const hintA = () => { const a = (d.adapters || []).find(x => x.name === f.agent.value); $("#agentHint", el).innerHTML = a ? `<span class="${a.warning ? "warn" : ""}">${esc(a.warning || a.command)}</span>` : "" };
    f.agent.onchange = hintA; hintA();
    gatesAll = (g.gates || []).map(x => x.name);
    const pinned = (sp.acceptance || []).map(x => x.name);
    $("#gbase", el).textContent = g.exists ? `${g.path} at ${g.base}` : g.error ? g.error : `no gates file at ${g.base}: defaults`;
    $("#gchecks", el).innerHTML = (g.gates || []).map(x => `<label class="check" title="${esc(x.tool)} (${esc(x.policy)})"><input type="checkbox" value="${esc(x.name)}" ${!pinned.length || pinned.includes(x.name) ? "checked" : ""}> ${esc(x.name)} <span class="hint">${esc(x.description)}</span></label>`).join("") || `<span class="muted">${esc(g.error || "none")}</span>`;
    templates = tp.templates || [];
    if (f.template) f.template.innerHTML = `<option value="">none</option>` + templates.map((x, i) => `<option value="${i}">${esc(x.name)}${x.source === "repo" ? " (repo)" : ""}${x.description ? " — " + esc(x.description) : ""}</option>`).join("");
    if (d.error) $("#tfErr", el).textContent = d.error;
  };
  if (f.repo.tagName === "SELECT") f.repo.onchange = fill;
  f.base.onchange = fill;
  if (f.template) f.template.onchange = () => {
    const x = templates[+f.template.value]; if (!x) return;
    if (x.intent) f.intent.value = x.intent; if (x.title) f.title.value = x.title;
    f.scope.value = (x.scope || []).join("\n"); if (x.agent) f.agent.value = x.agent;
    f.max_attempts.value = x.max_attempts || ""; f.max_rounds.value = x.max_rounds || ""; f.max_tokens.value = x.max_tokens || "";
    f.require_approval.checked = !!x.require_approval;
    $$("#gchecks input", el).forEach(c => c.checked = !(x.gates || []).length || x.gates.includes(c.value));
    const m = f.intent.value.match(/\{\{[^}]+\}\}/); if (m) { f.intent.focus(); f.intent.setSelectionRange(m.index, m.index + m[0].length) }
  };
  await fill(); f.intent.focus();
  const num = v => v === "" ? 0 : Number(v);
  const body = () => {
    const checked = $$("#gchecks input", el).filter(c => c.checked).map(c => c.value);
    return { intent: f.intent.value, title: f.title.value, scope: lines(f.scope.value), after: lines(f.after.value), base: f.base.value, agent: f.agent.value,
      require_approval: f.require_approval.checked, max_attempts: num(f.max_attempts.value), max_rounds: num(f.max_rounds.value), max_tokens: num(f.max_tokens.value),
      gates: checked.length === gatesAll.length ? [] : checked, template: f.template && templates[+f.template.value]?.name || "" };
  };
  $("#tfCancel", el).onclick = () => opts.onCancel();
  const st = $("#saveTpl", el);
  if (st) st.onclick = async () => {
    const name = prompt("Template name (e.g. “bugfix with tests”):"); if (!name) return;
    const b = body();
    try {
      await send("PUT", "/api/templates", { name, intent: b.intent, title: b.title, scope: b.scope, gates: b.gates, agent: b.agent, require_approval: b.require_approval, max_attempts: b.max_attempts, max_rounds: b.max_rounds, max_tokens: b.max_tokens });
      toast("Template saved", "ok"); fill();
    } catch (e) { toast(esc(e.message), "bad") }
  };
  f.onsubmit = async e => {
    e.preventDefault(); const btn = $("button[type=submit]", f); btn.disabled = true; $("#tfErr", el).textContent = "";
    try { await opts.onSubmit(f.repo.value, body()) } catch (x) { $("#tfErr", el).textContent = x.message } finally { btn.disabled = false }
  };
}
function viewNew() {
  $("#view").innerHTML = `<div class="page narrow"><h2>New task</h2><p class="hint">The agent works in an isolated worktree; the task integrates only if this repository's gates pass. For a larger goal, <a href="#/plan">plan it</a> first.</p><div id="nf"></div></div>`;
  taskForm($("#nf"), {
    onCancel: () => history.back(),
    onSubmit: async (rn, b) => {
      const j = await send("POST", `/api/repos/${enc(rn)}/tasks`, b);
      pendingNote[j.repo + "/" + j.id] = j.controller ? "Submitted; waiting for the controller to pick it up…" : "Submitted. No controller is running for this repository; it starts when one does (spine controller or spine supervisor).";
      filter = "all"; go(`tasks/${j.repo}/${j.id}`);
    }
  });
}
function editTask(r, t) {
  editing = true;
  $("#tab").innerHTML = `<p class="hint">${t.status.phase === "Pending" ? "Edits apply before the task starts (a new spec generation)." : "Saving starts a fresh attempt with the edited spec (a new generation)."}</p><div id="ef"></div>`;
  taskForm($("#ef"), {
    repo: r, task: t, onCancel: () => { editing = false; loadDetail() },
    onSubmit: async (rn, b) => { await send("POST", taskURL(r, t.id, "/edit"), { task: b }); editing = false; toast("Edit queued", "ok"); setTimeout(loadDetail, 800) }
  });
}

// ===== Plan =====
let planDraft = null; // {repo, plan_task, tasks:[...], notes}
const blankTask = () => ({ id: "", title: "", intent: "", scope: [], after: [], agent: "", gates: [] });
async function viewPlan() {
  const rn = curRepo();
  const plans = ov.tasks.filter(t => t.kind === "plan" && (repo === "all" || t.repo === repo));
  $("#view").innerHTML = `<div class="page narrow"><h2>Plan</h2>
   <p class="hint">Turn a goal into a set of small tasks with scopes and dependencies. A planner agent can propose them; you edit and validate before anything runs.</p>
   ${can("operator") ? `<section class="card"><h3>Ask a planner</h3><form class="f" id="pf">${ov.multi ? `<label>Repository<select name="repo">${ov.repos.map(r => `<option ${r.name === rn ? "selected" : ""}>${esc(r.name)}</option>`).join("")}</select></label>` : `<input type="hidden" name="repo" value="${esc(rn)}">`}
    <label>Goal<textarea name="goal" required placeholder="Add rate limiting to the public API: per-key token buckets, configurable limits, 429 responses with Retry-After, docs and tests."></textarea></label>
    <div class="row"><label>Planner agent<select name="agent"></select></label><label>Base branch<input name="base" placeholder="default"></label></div>
    <div class="actions"><button class="btn primary">Propose a plan</button><button type="button" class="btn" id="blank">Write a plan myself</button><button type="button" class="btn" id="import">Import JSON…</button></div></form></section>` : ""}
   <section class="card"><h3>Planner tasks</h3>${plans.length ? `<table>${plans.map(t => `<tr><td>${ov.multi ? `<span class="repo">${esc(t.repo)}</span> ` : ""}<a href="#/tasks/${enc(t.repo)}/${enc(t.id)}">${esc(t.title)}</a></td><td><span class="badge b-${esc(t.phase)}">${esc(t.phase)}</span> ${esc(t.reason)}</td><td>${ago(t.created)}</td>
     <td>${t.phase === "Escalated" && (t.reason === "PlanReady" || t.reason === "PlanInvalid") ? `<a class="btn small primary" href="#/tasks/${enc(t.repo)}/${enc(t.id)}/planreview">Review</a>` : ""}</td></tr>`).join("")}</table>` : `<p class="muted">None yet.</p>`}</section>
   <div id="editor"></div></div>`;
  const f = $("#pf");
  if (f) {
    const fillA = async () => { const d = await get(`/api/repos/${enc(f.repo.value)}/defaults`).catch(() => ({})); f.agent.innerHTML = (d.adapters || []).map(a => `<option value="${esc(a.name)}" ${a.name === d.agent ? "selected" : ""}>${esc(a.label)}</option>`).join("") };
    if (f.repo.tagName === "SELECT") f.repo.onchange = fillA; fillA();
    f.onsubmit = async e => {
      e.preventDefault();
      try { const j = await send("POST", `/api/repos/${enc(f.repo.value)}/plans`, { goal: f.goal.value, agent: f.agent.value, base: f.base.value }); toast("Planner started; the plan comes back for review.", "ok"); go(`tasks/${j.repo}/${j.id}/live`) }
      catch (x) { toast(esc(x.message), "bad") }
    };
    $("#blank").onclick = () => { planDraft = { repo: f.repo.value, tasks: [blankTask()], notes: "" }; planEditor($("#editor")) };
    $("#import").onclick = () => dialog(`<h2>Import a plan</h2><form class="f" id="imf"><label>Plan JSON (<code>{"tasks":[…]}</code>)<textarea name="j" style="min-height:240px"></textarea></label><div class="err" id="imErr"></div><div class="actions"><button class="btn primary">Load</button></div></form>`, b => {
      $("#imf", b).onsubmit = e => { e.preventDefault(); try { const p = JSON.parse(e.target.j.value); planDraft = { repo: f.repo.value, tasks: p.tasks || [], notes: p.notes || "" }; closeDialog(); planEditor($("#editor")) } catch (x) { $("#imErr", b).textContent = x.message } };
    });
  }
  if (planDraft && !planDraft.plan_task) planEditor($("#editor"));
}
function tabPlanReview(r, t) {
  planDraft = { repo: r, plan_task: t.status.phase === "Escalated" ? t.id : "", notes: t.status.plan?.notes || "", tasks: JSON.parse(JSON.stringify(t.status.plan?.tasks || [])), base: (t.spec.base_ref || "").replace("refs/heads/", "") };
  $("#tab").innerHTML = `<div id="editor"></div>`; planEditor($("#editor"), !planDraft.plan_task || !can("operator"));
}
let valTimer = null;
function planEditor(el, readonly) {
  const p = planDraft, ro = readonly ? "disabled" : "";
  el.innerHTML = `<h3 class="sec">Plan for ${esc(p.repo)} — ${p.tasks.length} task(s)</h3>${p.notes ? `<section class="card"><h3>Planner notes</h3><pre style="font-family:inherit">${esc(p.notes)}</pre></section>` : ""}
   <div id="pgraph"></div><div class="issues" id="pissues"></div>
   <div id="ptasks">${p.tasks.map((t, i) => `<div class="ptask" data-i="${i}">
     <div class="row"><label>ID<input data-k="id" value="${esc(t.id)}" ${ro} placeholder="add-rate-limiter"></label><label>Title<input data-k="title" value="${esc(t.title || "")}" ${ro}></label></div>
     <label>Intent<textarea data-k="intent" ${ro}>${esc(t.intent)}</textarea></label>
     <div class="row2"><label>Scope (one glob per line)<textarea data-k="scope" ${ro} style="min-height:50px">${esc((t.scope || []).join("\n"))}</textarea></label>
     <label>After (ids)<textarea data-k="after" ${ro} style="min-height:50px">${esc((t.after || []).join("\n"))}</textarea></label>
     <label>Agent / gates<input data-k="agent" value="${esc(t.agent || "")}" placeholder="default agent" ${ro}><input data-k="gates" value="${esc((t.gates || []).join(", "))}" placeholder="all gates" ${ro}></label></div>
     <div class="issues"></div>${readonly ? "" : `<div><button class="btn small danger" data-del="${i}">Remove</button></div>`}</div>`).join("")}</div>
   ${readonly ? `<p class="hint">${!can("operator") ? "Read-only." : "This plan was already handled."}</p>` : `<div class="actions"><button class="btn" id="padd">+ Add task</button><button class="btn" id="pexport">Export JSON</button>
     <label class="check hint"><input type="checkbox" id="pappr"> hold every task for my approval</label><button class="btn primary" id="psubmit">Submit ${p.tasks.length} task(s)</button>
     ${p.plan_task ? `<button class="btn" id="preplan">Ask the planner again…</button>` : ""}</div>`}`;
  const sync = () => {
    $$(".ptask", el).forEach(div => {
      const t = p.tasks[+div.dataset.i];
      $$("[data-k]", div).forEach(inp => { const k = inp.dataset.k, v = inp.value; t[k] = ["scope", "after"].includes(k) ? lines(v) : k === "gates" ? lines(v) : v.trim() === "" && k !== "intent" ? "" : v });
    });
  };
  const validate = async () => {
    sync();
    try {
      const j = await send("POST", `/api/repos/${enc(p.repo)}/plans/validate`, { plan: { tasks: p.tasks.map(clean), notes: p.notes }, base: p.base || "" });
      const global = j.issues.filter(x => !x.task || !p.tasks.some((t, i) => (t.id || `#${i + 1}`) === x.task));
      $("#pissues", el).innerHTML = (j.ok ? `<div class="pass">✓ spec.validate: no errors${j.issues.length ? `, ${j.issues.length} warning(s)` : ""}</div>` : `<div class="fail">✗ spec.validate found errors</div>`) + global.map(issue).join("");
      $$(".ptask", el).forEach(div => { const id = p.tasks[+div.dataset.i]?.id || `#${+div.dataset.i + 1}`; $(".issues", div).innerHTML = j.issues.filter(x => x.task === id).map(issue).join("") });
      const s = $("#psubmit", el); if (s) s.disabled = !j.ok;
      drawPlanGraph($("#pgraph", el), p.tasks);
    } catch (e) { $("#pissues", el).innerHTML = `<div class="fail">${esc(e.message)}</div>` }
  };
  const issue = x => `<div class="${x.level === "error" ? "fail" : "warn"}">${x.level === "error" ? "✗" : "!"} ${x.field ? `<b>${esc(x.field)}</b>: ` : ""}${esc(x.message)}</div>`;
  $$("input,textarea", $("#ptasks", el)).forEach(i => i.oninput = () => { clearTimeout(valTimer); valTimer = setTimeout(validate, 500) });
  $$("[data-del]", el).forEach(b => b.onclick = () => { sync(); p.tasks.splice(+b.dataset.del, 1); planEditor(el) });
  const add = $("#padd", el); if (add) add.onclick = () => { sync(); p.tasks.push(blankTask()); planEditor(el) };
  const ex = $("#pexport", el); if (ex) ex.onclick = () => { sync(); dialog(`<h2>Plan JSON</h2><pre class="log">${esc(JSON.stringify({ notes: p.notes, tasks: p.tasks.map(clean) }, null, 2))}</pre><p class="hint">Submit it from the CLI with <code>spine plan submit FILE</code>.</p>`) };
  const rp = $("#preplan", el); if (rp) rp.onclick = () => taskAction("hint", p.repo, { id: p.plan_task, status: { phase: "Escalated" } });
  const sb = $("#psubmit", el);
  if (sb) sb.onclick = async () => {
    sync(); sb.disabled = true;
    const appr = $("#pappr", el).checked;
    try {
      const j = await send("POST", `/api/repos/${enc(p.repo)}/plans/submit`, { plan: { tasks: p.tasks.map(t => ({ ...clean(t), require_approval: appr || !!t.require_approval })), notes: p.notes }, plan_task: p.plan_task || "", base: p.base || "" });
      toast(`Submitted ${j.submitted.length} task(s)`, "ok"); planDraft = null; go("graph");
    } catch (e) { sb.disabled = false; toast(esc(e.message), "bad"); if (e.body?.issues) validate() }
  };
  validate();
}
const clean = t => { const o = { id: (t.id || "").trim(), intent: t.intent || "" }; for (const k of ["title", "agent", "template"]) if (t[k]) o[k] = t[k]; for (const k of ["scope", "after", "gates"]) if ((t[k] || []).length) o[k] = t[k]; for (const k of ["max_attempts", "max_rounds", "max_tokens", "require_approval"]) if (t[k]) o[k] = t[k]; return o };
function drawPlanGraph(el, tasks) {
  const nodes = tasks.map(t => ({ key: t.id, label: t.id || "(no id)", deps: (t.after || []).filter(d => tasks.some(x => x.id === d)), state: "ready" }));
  el.innerHTML = nodes.length ? `<div class="graphwrap">${dagSVG(nodes, {})}</div>` : "";
}

// ===== Graph =====
let graphBusy = false, graphDone = true;
const STATE_COLOR = { done: "var(--okbg)", running: "var(--runbg)", ready: "var(--panel)", blocked: "var(--line)", needs: "var(--warnbg)", failed: "var(--badbg)", missing: "var(--badbg)" };
function dagSVG(nodes, opt) {
  const byKey = Object.fromEntries(nodes.map(n => [n.key, n]));
  const level = {}; const lv = (k, d = 0) => { if (level[k] !== undefined) return level[k]; if (d > nodes.length) return 0; const n = byKey[k]; level[k] = 0; const v = n ? Math.max(0, ...n.deps.filter(x => byKey[x]).map(x => lv(x, d + 1) + 1)) : 0; level[k] = v; return v };
  nodes.forEach(n => lv(n.key));
  const cols = {}; nodes.forEach(n => (cols[level[n.key]] ||= []).push(n));
  Object.values(cols).forEach(c => c.sort((a, b) => (a.repo || "").localeCompare(b.repo || "") || a.key.localeCompare(b.key)));
  const W = 220, H = 54, GX = 70, GY = 16, pos = {};
  let maxRows = 0;
  Object.entries(cols).forEach(([l, c]) => { maxRows = Math.max(maxRows, c.length); c.forEach((n, i) => pos[n.key] = { x: 16 + l * (W + GX), y: 16 + i * (H + GY) }) });
  const width = 32 + (Object.keys(cols).length) * (W + GX) - GX, height = 32 + maxRows * (H + GY) - GY;
  let edges = "", boxes = "";
  for (const n of nodes) for (const d of n.deps) {
    const a = pos[d], b = pos[n.key]; if (!a || !b) continue;
    const crit = n.critical && byKey[d]?.critical, cross = (byKey[d]?.repo || n.repo) !== n.repo;
    const x1 = a.x + W, y1 = a.y + H / 2, x2 = b.x, y2 = b.y + H / 2, mx = (x1 + x2) / 2;
    edges += `<path d="M${x1},${y1} C${mx},${y1} ${mx},${y2} ${x2},${y2}" fill="none" stroke="${crit ? "var(--crit)" : "var(--muted)"}" stroke-width="${crit ? 2.5 : 1.2}" ${cross ? 'stroke-dasharray="5 4"' : ""} marker-end="url(#arr)"/>`;
  }
  for (const n of nodes) {
    const p = pos[n.key];
    boxes += `<g class="gnode ${n.critical ? "crit" : ""}" data-k="${esc(n.key)}" transform="translate(${p.x},${p.y})"><title>${esc(n.key)} — ${esc(n.phase || n.state)} ${esc(n.reason || "")}</title>
      <rect width="${W}" height="${H}" rx="8" fill="${STATE_COLOR[n.state] || "var(--panel)"}"/>
      <text x="10" y="20" font-size="12" font-weight="600" fill="currentColor">${esc(trunc(n.label, 30))}</text>
      <text x="10" y="38" font-size="11" fill="var(--muted)">${esc(trunc([opt.multi && n.repo, n.state, n.phase && n.phase !== n.state ? n.phase : ""].filter(Boolean).join(" · "), 34))}</text></g>`;
  }
  return `<svg width="${width}" height="${height}" viewBox="0 0 ${width} ${height}" style="color:var(--fg)" role="img" aria-label="Task dependency graph">
    <defs><marker id="arr" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z" fill="var(--muted)"/></marker></defs>${edges}${boxes}</svg>`;
}
const trunc = (s, n) => (s = String(s || ""), s.length > n ? s.slice(0, n - 1) + "…" : s);
async function viewGraph() {
  graphBusy = true;
  const q = `?done=${graphDone ? 1 : 0}${repo !== "all" ? "&repo=" + enc(repo) : ""}`;
  const g = await get("/api/graph" + q).catch(e => ({ error: e.message, nodes: [] }));
  graphBusy = false;
  if (route.view !== "graph") return;
  const nodes = g.nodes.map(n => ({ ...n, label: n.title && n.title !== n.id ? n.id + ": " + n.title : n.id }));
  const counts = {}; nodes.forEach(n => counts[n.state] = (counts[n.state] || 0) + 1);
  $("#view").innerHTML = `<div class="page"><h2>Dependency graph</h2>
   <p class="hint">Tasks and their <code>after</code> links${ov.multi ? ", across repositories (dashed)" : ""}. The <span style="color:var(--crit)">critical path</span> is the longest chain of unfinished work, about ${fmtMin(g.critical_minutes)} at the median task time (${fmtMin(g.estimate_minutes)}).</p>
   <div class="legend">${["running", "ready", "blocked", "needs", "failed", "done", "missing"].map(s => `<span><i style="background:${STATE_COLOR[s]};border:1px solid var(--line)"></i>${s} ${counts[s] || 0}</span>`).join("")}
   <label class="check"><input type="checkbox" id="gdone" ${graphDone ? "checked" : ""}> show finished tasks</label></div>
   ${g.error ? `<p class="fail">${esc(g.error)}</p>` : nodes.length ? `<div class="graphwrap">${dagSVG(nodes, { multi: ov.multi })}</div>` : `<div class="empty">No tasks.</div>`}
   <h3 class="sec">As a table</h3><table><tr><th>Task</th><th>State</th><th>After</th><th>Critical</th></tr>${nodes.map(n => `<tr><td><a href="#/tasks/${enc(n.repo)}/${enc(n.id)}">${esc(n.key)}</a></td><td><span class="badge b-${esc(n.state)}">${esc(n.state)}</span></td><td class="mono">${esc(n.deps.join(", "))}</td><td>${n.critical ? "yes" : ""}</td></tr>`).join("")}</table></div>`;
  $("#gdone").onchange = e => { graphDone = e.target.checked; viewGraph() };
  $$(".gnode").forEach(gn => gn.onclick = () => { const k = gn.dataset.k, i = k.indexOf("/"); if (nodes.find(n => n.key === k)?.missing) return; go(`tasks/${k.slice(0, i)}/${k.slice(i + 1)}`) });
}

// ===== Charts =====
// Vertical bars per day. series: [{key,label,color}]; stacked side by side.
function barChart(rows, series, opt = {}) {
  const W = 720, H = 200, L = 44, B = 22, T = 10, R = 8, n = rows.length;
  const max = Math.max(1, ...rows.map(r => series.reduce((a, s) => Math.max(a, r[s.key] || 0), 0)));
  const nice = niceMax(max), bw = (W - L - R) / n, gap = 2, sw = Math.max(2, (bw - 6) / series.length - gap);
  let g = "";
  for (let i = 0; i <= 4; i++) { const v = nice * i / 4, y = H - B - (H - B - T) * i / 4; g += `<line class="gridl" x1="${L}" x2="${W - R}" y1="${y}" y2="${y}"/><text class="axis" x="${L - 6}" y="${y + 3}" text-anchor="end">${opt.fmt ? opt.fmt(v) : fmtShort(v)}</text>` }
  rows.forEach((r, i) => {
    const x0 = L + i * bw + 3;
    series.forEach((s, j) => {
      const v = r[s.key] || 0; if (!v) return; const h = (H - B - T) * v / nice, x = x0 + j * (sw + gap), y = H - B - h, rad = Math.min(4, sw / 2, h);
      g += `<path d="M${x},${H - B} V${y + rad} Q${x},${y} ${x + rad},${y} H${x + sw - rad} Q${x + sw},${y} ${x + sw},${y + rad} V${H - B} Z" fill="${s.color}"><title>${esc(r.day)} · ${esc(s.label)}: ${opt.fmt ? opt.fmt(v) : fmtN(v)}</title></path>`;
    });
    if (n <= 16 || i % Math.ceil(n / 12) === 0) g += `<text class="axis" x="${x0 + bw / 2 - 3}" y="${H - 6}" text-anchor="middle">${esc(r.day.slice(5))}</text>`;
  });
  const legend = series.length > 1 ? `<div class="legend">${series.map(s => `<span><i style="background:${s.color}"></i>${esc(s.label)}</span>`).join("")}</div>` : "";
  return legend + `<svg class="chart" viewBox="0 0 ${W} ${H}" role="img" aria-label="${esc(opt.label || "chart")}">${g}</svg>`;
}
const niceMax = m => { const p = Math.pow(10, Math.floor(Math.log10(m))); for (const k of [1, 2, 2.5, 5, 10]) if (k * p >= m) return k * p; return m };
const fmtShort = v => v >= 1e6 ? (v / 1e6).toFixed(1).replace(/\.0$/, "") + "M" : v >= 1e3 ? (v / 1e3).toFixed(1).replace(/\.0$/, "") + "k" : String(Math.round(v * 100) / 100);
function hbars(items, opt = {}) {
  if (!items.length) return `<p class="muted">None.</p>`;
  const max = Math.max(...items.map(x => x.count));
  return `<div class="bars">${items.slice(0, opt.n || 10).map(x => `<div class="hbar"><span class="mono" title="${esc(x.name)}">${esc(trunc(x.name || "(none)", 40))}</span><div class="track"><div class="fillb" style="width:${(100 * x.count / max).toFixed(1)}%"></div></div><span class="num">${fmtN(x.count)}</span></div>`).join("")}</div>`;
}
const tile = (n, l, cls = "", href = "") => `<div class="tile ${cls}">${href ? `<a href="${href}">` : ""}<div class="n">${n}</div><div class="l">${l}</div>${href ? "</a>" : ""}</div>`;
let statDays = 14;
async function loadStats() { return get(`/api/stats?days=${statDays}${repo !== "all" ? "&repo=" + enc(repo) : ""}`) }
const daysPicker = () => `<label class="hint">Period <select id="days">${[7, 14, 30, 90].map(d => `<option ${d === statDays ? "selected" : ""} value="${d}">last ${d} days</option>`).join("")}</select></label>`;
const bindDays = fn => { $("#days").onchange = e => { statDays = +e.target.value; fn() } };

async function viewDash() {
  const s = await loadStats().catch(e => ({ error: e.message }));
  if (route.view !== "dash") return;
  if (s.error) { $("#view").innerHTML = `<div class="page"><p class="fail">${esc(s.error)}</p></div>`; return }
  const T = s.totals, W = s.waits;
  $("#view").innerHTML = `<div class="page"><h2>Project</h2>${daysPicker()}
   <div class="tiles">${tile(T.in_flight || 0, "in flight", "", "#/tasks")}${tile(T.needs_you || 0, "waiting on you", T.needs_you ? "alert" : "", "#/tasks")}${tile(T.pending || 0, "queued")}
    ${tile(T.merged_today || 0, "merged today")}${tile(T.failed_today || 0, "failed today", T.failed_today ? "alert" : "")}${tile(fmtMin(W.queue_p50), "median wait to start")}
    ${tile(fmtMin(W.approval_p50), "median wait for approval")}${tile(fmtMin(W.escalated_age_max), "oldest escalation")}</div>
   <section class="card"><h3>Throughput per day</h3>${barChart(s.per_day, [{ key: "integrated", label: "integrated", color: "var(--s1)" }, { key: "failed", label: "failed", color: "var(--s2)" }], { label: "Tasks integrated and failed per day" })}</section>
   <div class="grid2"><section class="card"><h3>Repositories</h3><table><tr><th>Repository</th><th class="num">In flight</th><th class="num">Queued</th><th class="num">Needs you</th><th class="num">Integrated</th><th class="num">Failed</th></tr>
    ${s.repos.map(r => `<tr><td>${esc(r.repo)}</td><td class="num">${r.in_flight}</td><td class="num">${r.pending}</td><td class="num">${r.needs_you}</td><td class="num">${r.integrated}</td><td class="num">${r.failed}</td></tr>`).join("")}</table></section>
   <section class="card"><h3>Waiting times (minutes)</h3><table><tr><th></th><th class="num">median</th><th class="num">p90</th></tr><tr><td>Created → started</td><td class="num">${fmtMin(W.queue_p50)}</td><td class="num">${fmtMin(W.queue_p90)}</td></tr>
    <tr><td>Verified → approved</td><td class="num">${fmtMin(W.approval_p50)}</td><td class="num">${fmtMin(W.approval_p90)}</td></tr></table></section></div>
   <details><summary class="hint">Per-day table</summary><table><tr><th>Day</th><th class="num">Created</th><th class="num">Integrated</th><th class="num">Failed</th><th class="num">Agent runs</th></tr>${s.per_day.map(d => `<tr><td>${d.day}</td><td class="num">${d.created}</td><td class="num">${d.integrated}</td><td class="num">${d.failed}</td><td class="num">${d.runs}</td></tr>`).join("")}</table></details></div>`;
  bindDays(viewDash);
}
async function viewCost() {
  const s = await loadStats().catch(e => ({ error: e.message }));
  if (route.view !== "cost") return;
  if (s.error) { $("#view").innerHTML = `<div class="page"><p class="fail">${esc(s.error)}</p></div>`; return }
  const c = s.cost, L = c.limits || {};
  const over = (v, l) => l && v >= l ? "alert" : "";
  $("#view").innerHTML = `<div class="page"><h2>Cost and tokens</h2>${daysPicker()}
   <div class="tiles">${tile(fmtN(c.today.tokens), "tokens today" + (L.daily_tokens ? ` of ${fmtShort(L.daily_tokens)}` : ""), over(c.today.tokens, L.daily_tokens))}
    ${tile(fmtUSD(c.today.cost_usd), "cost today" + (L.daily_cost_usd ? ` of ${fmtUSD(L.daily_cost_usd)}` : ""), over(c.today.cost_usd, L.daily_cost_usd))}
    ${tile(fmtShort(c.tokens), `tokens, ${statDays} days`)}${tile(fmtUSD(c.cost_usd), `cost, ${statDays} days`)}${tile(c.over_budget.length, "tasks near their budget", c.over_budget.length ? "alert" : "")}</div>
   <p class="hint">Cost comes from agents that report it (Pi does; Claude Code over ACP reports tokens only). Budgets and alert thresholds are set in <a href="#/settings">Settings → Notifications</a>.</p>
   <section class="card"><h3>Tokens per day</h3>${barChart(s.per_day, [{ key: "tokens", label: "tokens", color: "var(--s1)" }], { label: "Tokens per day" })}</section>
   ${c.cost_usd ? `<section class="card"><h3>Cost per day (USD)</h3>${barChart(s.per_day, [{ key: "cost_usd", label: "cost", color: "var(--s1)" }], { label: "Cost per day", fmt: v => "$" + fmtShort(v) })}</section>` : ""}
   <div class="grid2"><section class="card"><h3>Per repository</h3><table><tr><th>Repository</th><th class="num">Tokens (all time)</th><th class="num">Cost (${statDays}d)</th></tr>${s.repos.map(r => `<tr><td>${esc(r.repo)}</td><td class="num">${fmtN(r.tokens)}</td><td class="num">${fmtUSD(r.cost_usd)}</td></tr>`).join("")}</table></section>
   <section class="card"><h3>Near or over budget</h3>${c.over_budget.length ? `<table>${c.over_budget.map(t => `<tr><td><a href="#/tasks/${enc(t.repo)}/${enc(t.id)}">${esc(t.title)}</a></td><td class="num">${fmtN(t.tokens)} / ${fmtN(t.max_tokens)}</td><td><span class="badge b-${esc(t.phase)}">${esc(t.phase)}</span></td></tr>`).join("")}</table>` : `<p class="muted">No task is near its token budget.</p>`}</section></div>
   <section class="card"><h3>Most expensive tasks</h3><table><tr><th>Task</th><th class="num">Tokens</th><th class="num">Cost</th><th class="num">Agent runs</th><th class="num">Per run</th><th>Phase</th></tr>${c.top_tasks.map(t => `<tr><td>${ov.multi ? `<span class="repo">${esc(t.repo)}</span> ` : ""}<a href="#/tasks/${enc(t.repo)}/${enc(t.id)}/rounds">${esc(t.title)}</a></td><td class="num">${fmtN(t.tokens)}${t.max_tokens ? ` <span class="muted">/ ${fmtShort(t.max_tokens)}</span>` : ""}</td><td class="num">${fmtUSD(t.cost_usd)}</td><td class="num">${t.rounds}</td><td class="num">${t.rounds ? fmtN(Math.round(t.tokens / t.rounds)) : "—"}</td><td><span class="badge b-${esc(t.phase)}">${esc(t.phase)}</span></td></tr>`).join("")}</table></section></div>`;
  bindDays(viewCost);
}
async function viewQuality() {
  const s = await loadStats().catch(e => ({ error: e.message }));
  if (route.view !== "quality") return;
  if (s.error) { $("#view").innerHTML = `<div class="page"><p class="fail">${esc(s.error)}</p></div>`; return }
  const q = s.quality;
  $("#view").innerHTML = `<div class="page"><h2>Quality</h2>${daysPicker()}
   <div class="tiles">${tile(q.finished ? Math.round(100 * q.first_round_rate) + "%" : "—", `first-round pass (${q.first_round_pass}/${q.finished})`)}${tile(q.avg_rounds.toFixed(1), "agent runs per finished task")}
    ${tile(q.avg_attempts.toFixed(1), "attempts per finished task")}${tile(q.finished, "finished tasks")}</div>
   <div class="grid2"><section class="card"><h3>Why tasks escalated</h3>${hbars(q.escalations)}</section><section class="card"><h3>Top failing gates</h3>${hbars(q.failing_gates)}</section>
   <section class="card"><h3>Why tasks failed</h3>${hbars(q.fail_reasons)}</section><section class="card"><h3>How agent runs ended</h3>${hbars(q.outcome_reasons)}</section></div>
   <section class="card"><h3>Supervisor decisions: LLM vs rules</h3><p class="hint">Each failed round goes to a supervisor (rules, or the LLM with a rules fallback). “Improved” counts decisions whose next gated round failed fewer checks — the M3 comparison.</p>
   ${q.supervisors.length ? `<table><tr><th>Supervisor</th><th class="num">Decisions</th><th>Actions</th><th class="num">Fallbacks</th><th class="num">Improved next round</th><th class="num">Tasks</th><th class="num">…integrated</th></tr>
    ${q.supervisors.map(x => `<tr><td class="mono">${esc(x.supervisor)}</td><td class="num">${x.decisions}</td><td>${Object.entries(x.actions).map(([k, v]) => `${esc(k)} ${v}`).join(", ")}</td><td class="num">${x.fallbacks}</td>
     <td class="num">${x.followed ? Math.round(100 * x.improved / x.followed) + "%" : "—"} <span class="muted">(${x.improved}/${x.followed})</span></td><td class="num">${x.tasks}</td><td class="num">${x.tasks ? Math.round(100 * x.integrated / x.tasks) + "%" : "—"}</td></tr>`).join("")}</table>` : `<p class="muted">No decisions in this period.</p>`}</section></div>`;
  bindDays(viewQuality);
}

// ===== Supervisor / slots =====
async function viewSlots() {
  const [s, st] = await Promise.all([get("/api/slots").catch(e => ({ error: e.message, repos: [] })), get("/api/admin/supervisor").catch(() => ({}))]);
  if (route.view !== "slots") return;
  const gl = s.global;
  const gauge = (k, label) => { const max = gl.max[k] || 0, use = gl.in_use[k] || 0; return `<div><b>${label}</b> ${use} / ${max || "∞"} <span class="muted">(peak ${gl.peak[k] || 0})</span><div class="gauge ${max && use >= max ? "full" : ""}"><div style="width:${max ? Math.min(100, 100 * use / max) : 0}%"></div></div></div>` };
  $("#view").innerHTML = `<div class="page"><h2>Supervisor and slots</h2>
   ${gl ? `<div class="grid2"><section class="card"><h3>Global slots</h3>${gauge("agent", "Agent runs")}${gauge("gate", "Gate evaluations")}
     ${can("admin") ? `<form class="f" id="limf" style="margin-top:10px"><div class="row"><label>Max agents<input name="a" type="number" min="1" max="256" value="${st.max_agents || ""}"></label><label>Max gates<input name="g" type="number" min="1" max="256" value="${st.max_gates || ""}"></label></div><div><button class="btn small">Apply now and save</button></div></form>` : ""}</section>
     <section class="card"><h3>Queue for a global slot</h3>${gl.waiting.length ? `<table>${gl.waiting.map(w => `<tr><td>${esc(w.kind)}</td><td><a href="#/tasks/${enc(w.repo)}/${enc(w.task)}">${esc(w.repo)}/${esc(w.task)}</a></td><td>waiting ${ago(w.since)}</td></tr>`).join("")}</table>` : `<p class="muted">Nothing is waiting.</p>`}
     <h3 style="margin-top:10px">Holding a slot</h3>${gl.holders.length ? `<table>${gl.holders.map(w => `<tr><td>${esc(w.kind)}</td><td><a href="#/tasks/${enc(w.repo)}/${enc(w.task)}">${esc(w.repo)}/${esc(w.task)}</a></td><td>${ago(w.since)}</td></tr>`).join("")}</table>` : `<p class="muted">Idle.</p>`}</section></div>`
      : `<p class="hint">Global agent/gate limits exist under <code>spine supervisor</code>; this dashboard serves one repository.</p>`}
   <h3 class="sec">Repositories</h3><table><tr><th>Repository</th><th>State</th><th class="num">Restarts</th><th>Worktree slots</th><th>Ready, waiting for a slot</th><th class="num">Blocked on deps</th><th></th></tr>
   ${s.repos.map(r => `<tr><td>${esc(r.repo)}</td><td><span class="badge ${r.state === "running" ? "b-Integrated" : r.state === "broken" ? "b-Failed" : ""}">${esc(r.state)}</span>${r.paused ? ' <span class="badge b-Escalated">paused</span>' : ""}${r.error ? `<div class="diag">${esc(r.error)}</div>` : ""}<div class="muted">${r.since && !r.since.startsWith("0001") ? "since " + ago(r.since) : ""}</div></td>
    <td class="num">${r.restarts}</td><td>${r.held.length} / ${r.parallel}<div class="gauge ${r.held.length >= r.parallel ? "full" : ""}"><div style="width:${r.parallel ? Math.min(100, 100 * r.held.length / r.parallel) : 0}%"></div></div><div class="mono muted">${r.held.map(esc).join(", ")}</div></td>
    <td class="mono">${r.ready.map(id => `<a href="#/tasks/${enc(r.repo)}/${enc(id)}">${esc(id)}</a>`).join(", ") || '<span class="muted">—</span>'}</td><td class="num">${r.blocked}</td>
    <td>${can("operator") ? (r.paused ? `<button class="btn small" data-resume="${esc(r.repo)}">Resume</button>` : `<button class="btn small" data-pause="${esc(r.repo)}">Pause</button>`) : ""}</td></tr>`).join("")}</table>
   <p class="hint">Pausing a repository lets running tasks finish and starts no new one. Configuration changes restart its controller (Settings).</p></div>`;
  const lf = $("#limf"); if (lf) lf.onsubmit = async e => { e.preventDefault(); try { await send("PUT", "/api/admin/supervisor", { max_agents: +lf.a.value, max_gates: +lf.g.value }); toast("Limits applied", "ok"); viewSlots() } catch (x) { toast(esc(x.message), "bad") } };
  $$("[data-pause]").forEach(b => b.onclick = async () => { const reason = prompt("Reason (optional):") ?? null; if (reason === null) return; try { await send("POST", `/api/repos/${enc(b.dataset.pause)}/pause`, { reason }); viewSlots() } catch (x) { toast(esc(x.message), "bad") } });
  $$("[data-resume]").forEach(b => b.onclick = async () => { try { await send("POST", `/api/repos/${enc(b.dataset.resume)}/resume`); viewSlots() } catch (x) { toast(esc(x.message), "bad") } });
}

// ===== Health =====
async function viewHealth(probe) {
  const rn = curRepo();
  $("#view").innerHTML = `<div class="page narrow"><h2>Health — ${esc(rn)}</h2><p class="hint">Runs <code>spine doctor</code>${probe ? " with the agent handshake" : ""}, and checks disk, database size and stuck work.${ov.multi ? " Pick another repository in the header." : ""}</p><div id="hb" class="muted">Running checks…</div></div>`;
  const h = await get(`/api/repos/${enc(rn)}/health${probe ? "?probe=1" : ""}`).catch(e => ({ error: e.message }));
  if (route.view !== "health") return;
  if (h.error) { $("#hb").innerHTML = `<p class="fail">${esc(h.error)}</p>`; return }
  const icon = l => l === "ok" ? `<span class="pass">✓ ok</span>` : l === "warn" ? `<span class="warn">! warn</span>` : `<span class="fail">✗ fail</span>`;
  const gc = h.gc;
  $("#hb").innerHTML = `<div class="actions"><button class="btn" id="probe">Check the agent handshake too</button><button class="btn" id="rerun">Re-run</button></div>
   <div class="tiles">${tile(fmtBytes(h.db_bytes), "database")}${tile(fmtBytes(h.wal_bytes), "WAL", h.wal_bytes > 256 << 20 ? "alert" : "")}${tile(fmtBytes(h.disk_free), "disk free of " + fmtBytes(h.disk_total), h.disk_total && h.disk_free * 20 < h.disk_total ? "alert" : "")}
    ${tile(h.inbox_queue, "commands queued", h.inbox_queue > 20 ? "alert" : "")}${tile(h.stuck.length, "stuck items", h.stuck.length ? "alert" : "")}</div>
   <section class="card"><h3>spine doctor</h3><table>${h.findings.map(f => `<tr><td>${icon(f.level)}</td><td class="mono">${esc(f.check)}</td><td>${esc(f.message)}</td></tr>`).join("")}</table></section>
   <section class="card"><h3>Stuck leases, worktrees and tasks</h3>${h.stuck.length ? h.stuck.map(x => `<div class="warn">${esc(x)}</div>`).join("") : `<p class="muted">Nothing stuck.</p>`}
    ${h.rejected.length ? `<p class="hint">Rejected inbox commands: ${h.rejected.map(esc).join(", ")}</p>` : ""}</section>
   ${gc ? `<section class="card"><h3>Garbage collection</h3><table><tr><td>Snapshot refs of tasks finished over <input type="number" id="gcd" min="1" value="${gc.days}" style="width:60px"> days ago</td><td class="num">${gc.snapshot_refs.length}</td></tr>
     <tr><td>Stray worktrees</td><td class="num">${gc.stray_worktrees.length}</td></tr><tr><td>Tool cache (older / total)</td><td class="num">${fmtBytes(gc.old_cache_bytes)} / ${fmtBytes(gc.tool_cache_bytes)} (${gc.old_cache_rows}/${gc.tool_cache_rows} entries)</td></tr>
     <tr><td>Artifacts (gate logs)</td><td class="num">${fmtBytes(gc.artifact_bytes)}</td></tr></table>
     ${gc.snapshot_refs.length ? `<details><summary class="hint">refs</summary><pre class="log">${esc(gc.snapshot_refs.join("\n"))}</pre></details>` : ""}
     ${can("admin") ? `<div class="actions"><button class="btn" id="gcplan">Recount</button><button class="btn primary" id="gcrun">Clean up</button></div>` : ""}</section>` : ""}`;
  $("#probe").onclick = () => viewHealth(true); $("#rerun").onclick = () => viewHealth(probe);
  const gp = $("#gcplan"); if (gp) gp.onclick = async () => { const r = await get(`/api/repos/${enc(rn)}/gc?days=${$("#gcd").value}`); toast(`${r.snapshot_refs.length} refs, ${r.stray_worktrees.length} worktrees, ${fmtBytes(r.old_cache_bytes)} cache would be removed`) };
  const gr = $("#gcrun"); if (gr) gr.onclick = async () => { if (!confirm("Remove old snapshot refs, stray worktrees and old cache entries?")) return; try { const j = await send("POST", `/api/repos/${enc(rn)}/gc`, { days: +$("#gcd").value }); toast(j.controller ? "Cleanup queued for the controller" : "Queued; it runs when the controller starts", "ok") } catch (x) { toast(esc(x.message), "bad") } };
}

// ===== Audit =====
async function viewAudit() {
  const a = await get("/api/audit?limit=1000").catch(e => ({ error: e.message, entries: [] }));
  if (route.view !== "audit") return;
  $("#view").innerHTML = `<div class="page"><h2>Audit trail</h2><p class="hint">Every change made through this dashboard, with who made it${a.path ? ` (<code>${esc(a.path)}</code>)` : ""}. Task commands are also in each task's timeline.</p>
   <div class="actions"><input id="af" placeholder="filter: user, repo, task, action"></div>
   ${a.error ? `<p class="fail">${esc(a.error)}</p>` : ""}<table id="at"><tr><th>When</th><th>User</th><th>Action</th><th>Repository / task</th><th>Detail</th><th class="num">Result</th><th>From</th></tr>
   ${a.entries.map(e => `<tr data-s="${esc((e.user + " " + e.repo + " " + e.task + " " + e.path + " " + e.detail).toLowerCase())}"><td>${new Date(e.at).toLocaleString()}</td><td>${esc(e.user)} <span class="muted">${esc(e.role)}${e.authenticated ? "" : " (local)"}</span></td>
    <td class="mono">${esc(e.method)} ${esc(e.path.replace(/^\/api\//, ""))}</td><td>${e.task ? `<a href="#/tasks/${enc(e.repo)}/${enc(e.task)}">${esc(e.repo)}/${esc(e.task)}</a>` : esc(e.repo || "")}</td><td>${esc(e.detail || "")}</td>
    <td class="num ${e.status >= 400 ? "fail" : "pass"}">${e.status}</td><td class="mono muted">${esc(e.remote)}</td></tr>`).join("")}</table></div>`;
  $("#af").oninput = e => { const q = e.target.value.toLowerCase(); $$("#at tr[data-s]").forEach(r => r.hidden = q && !r.dataset.s.includes(q)) };
}

// ===== Settings =====
async function viewSettings() {
  const rn = curRepo();
  $("#view").innerHTML = `<div class="page narrow"><h2>Settings</h2>
   ${me.admin ? `<section class="card"><h3>Repositories</h3><table>${ov.repos.map(r => `<tr><td>${esc(r.name)}</td><td class="mono muted">${esc(r.path)}</td><td><span class="badge">${esc(r.state)}</span></td><td>${can("admin") ? `<button class="btn small danger" data-rm="${esc(r.name)}">Remove</button>` : ""}</td></tr>`).join("")}</table>
    ${can("admin") ? `<form class="f" id="addr" style="margin-top:10px"><div class="row"><label>Path to a git checkout<input name="path" required placeholder="/home/me/src/api"></label><label>Name (optional)<input name="name"></label></div><div><button class="btn">Add repository</button></div></form>` : ""}
    <p class="hint">Removing a repository stops its controller; its tasks and data stay in the repository.</p></section>` : ""}
   <section class="card"><h3>Repository configuration — ${esc(rn)}</h3><div id="cfg" class="muted">Loading…</div></section>
   <section class="card"><h3>Notifications</h3><div id="ntf" class="muted">${me.home ? "Loading…" : "Not available."}</div></section>
   <section class="card"><h3>Templates</h3><div id="tpl" class="muted">Loading…</div></section>
   <section class="card"><h3>Access</h3><p>${me.auth ? `Sign-in is required. You are <b>${esc(me.user?.name)}</b> (${esc(me.user?.role)}).` : `No users exist: the dashboard serves loopback only and records changes as the local user.`}${me.read_only ? " This dashboard is <b>read-only</b>." : ""}</p>
    <p class="hint">Manage users with <code>spine users add NAME --role viewer|operator|admin</code> (prints a token once), <code>spine users list</code> and <code>spine users remove NAME</code>. Serving a non-loopback address (<code>--addr 0.0.0.0:8765</code>) requires users; add <code>--tls-cert/--tls-key</code> for HTTPS and <code>--read-only</code> for a view-only dashboard.</p></section></div>`;
  $$("[data-rm]").forEach(b => b.onclick = async () => { if (!confirm(`Stop supervising ${b.dataset.rm}?`)) return; try { await send("DELETE", `/api/admin/repos/${enc(b.dataset.rm)}`); toast("Removed", "ok") } catch (x) { toast(esc(x.message), "bad") } });
  const ar = $("#addr"); if (ar) ar.onsubmit = async e => { e.preventDefault(); try { await send("POST", "/api/admin/repos", { path: ar.path.value, name: ar.name.value }); toast("Added; its controller is starting", "ok"); ar.reset() } catch (x) { toast(esc(x.message), "bad") } };
  settingsConfig(rn); settingsNotify(); settingsTemplates(rn);
}
async function settingsConfig(rn) {
  const c = await get(`/api/repos/${enc(rn)}/config`).catch(e => ({ error: e.message }));
  const s = c.settings || {}, ro = can("admin") ? "" : "disabled";
  $("#cfg").className = "";
  $("#cfg").innerHTML = `${c.error ? `<p class="fail">${esc(c.error)}</p>` : ""}<p class="hint"><code>${esc(c.path || "")}</code>${c.exists ? "" : " (absent: built-in defaults)"} · ${c.restart ? "saving restarts this repository's controller" : "restart <code>spine controller</code> to apply changes"}</p>
   <form class="f" id="cf"><div class="row3"><label>Default agent<select name="agent" ${ro}>${(c.adapters || []).map(a => `<option value="${esc(a.name)}" ${a.name === s.agent ? "selected" : ""}>${esc(a.label)}</option>`).join("")}</select></label>
    <label>Parallel tasks<input name="parallel" type="number" min="1" value="${s.parallel || ""}" ${ro}></label><label>Base branch<input name="base" value="${esc(s.base || "")}" ${ro}></label></div>
    <div class="row3"><label>Max attempts<input name="max_attempts" type="number" min="1" value="${s.max_attempts || ""}" ${ro}></label><label>Max repair rounds<input name="max_patch_rounds" type="number" min="0" value="${s.max_patch_rounds ?? ""}" ${ro}></label>
    <label>Token budget per task<input name="max_tokens" type="number" min="0" value="${s.max_tokens || 0}" ${ro}></label></div>
    <div class="row3"><label>Run timeout<input name="per_run_timeout" value="${esc(s.per_run_timeout || "")}" ${ro}></label><label>Wall clock<input name="max_wall_clock" value="${esc(s.max_wall_clock || "")}" ${ro}></label>
    <label>Supervisor<select name="supervisor" ${ro}>${["rules", "llm"].map(x => `<option ${x === s.supervisor ? "selected" : ""}>${x}</option>`).join("")}</select></label></div>
    ${ro ? "" : `<div class="actions"><button class="btn primary">Save settings</button><button type="button" class="btn" id="rawBtn">Edit YAML…</button></div>`}<div class="err" id="cfErr"></div></form>
    <details><summary class="hint">Agents</summary><table>${(c.adapters || []).map(a => `<tr><td>${esc(a.name)}</td><td>${esc(a.label)}</td><td class="mono">${esc(a.command)}</td><td class="warn">${esc(a.warning || "")}</td></tr>`).join("")}</table></details>`;
  const f = $("#cf");
  f.onsubmit = async e => {
    e.preventDefault(); $("#cfErr").textContent = "";
    const settings = { agent: f.agent.value, parallel: +f.parallel.value, base: f.base.value, max_attempts: +f.max_attempts.value, max_patch_rounds: +f.max_patch_rounds.value,
      max_tokens: +f.max_tokens.value, per_run_timeout: f.per_run_timeout.value, max_wall_clock: f.max_wall_clock.value, supervisor: f.supervisor.value };
    try { const j = await send("PUT", `/api/repos/${enc(rn)}/config`, { settings }); toast(j.changed ? (j.restarted ? "Saved; controller restarted" : "Saved") : "No changes", "ok"); settingsConfig(rn) } catch (x) { $("#cfErr").textContent = x.message }
  };
  const rb = $("#rawBtn");
  if (rb) rb.onclick = () => dialog(`<h2>.spine/config.yaml</h2><form class="f" id="rawf"><textarea name="raw" style="min-height:360px;font-family:ui-monospace,monospace;font-size:12px">${esc(c.raw || "")}</textarea><div class="err" id="rawErr"></div>
    <div class="actions"><button class="btn primary">Validate and save</button><button type="button" class="btn" id="dc">Cancel</button></div></form>`, b => {
    $("#dc", b).onclick = closeDialog;
    $("#rawf", b).onsubmit = async e => { e.preventDefault(); try { await send("PUT", `/api/repos/${enc(rn)}/config`, { raw: e.target.raw.value }); closeDialog(); toast("Saved", "ok"); settingsConfig(rn) } catch (x) { $("#rawErr", b).textContent = x.message } };
  });
}
async function settingsNotify() {
  if (!me.home) return;
  if (!can("admin")) { $("#ntf").innerHTML = `<p class="muted">Admins set notification sinks. Browser notifications: use the 🔔 button.</p>`; return }
  const n = await get("/api/notify/config").catch(e => ({ error: e.message }));
  const c = n.config || {}, kinds = n.kinds || [], on = (c.events && c.events.length) ? c.events : (n.default_kinds || []);
  $("#ntf").className = "";
  $("#ntf").innerHTML = `${n.error ? `<p class="fail">${esc(n.error)}</p>` : ""}<form class="f" id="nf"><div class="gatechecks">${kinds.map(k => `<label class="check"><input type="checkbox" name="ev" value="${k}" ${on.includes(k) ? "checked" : ""}> ${k}</label>`).join("")}</div>
   <label class="check"><input type="checkbox" name="desktop" ${c.desktop ? "checked" : ""}> Desktop notifications on the machine running Spine (notify-send / osascript)</label>
   <label><span>Webhooks — one URL per line. Slack-compatible <code>{"text"}</code> payloads; add <code>generic</code> after a URL for a JSON event.</span><textarea name="hooks" style="min-height:60px">${esc((c.webhooks || []).map(w => w.url + (w.format === "generic" ? " generic" : "")).join("\n"))}</textarea></label>
   <div class="row"><label>Email: SMTP host<input name="host" value="${esc(c.email?.host || "")}"></label><label>Port<input name="port" type="number" value="${c.email?.port || ""}" placeholder="587"></label></div>
   <div class="row"><label>Username<input name="user" value="${esc(c.email?.username || "")}"></label><label>Password environment variable<input name="penv" value="${esc(c.email?.password_env || "")}" placeholder="SPINE_SMTP_PASSWORD"></label></div>
   <div class="row"><label>From<input name="from" value="${esc(c.email?.from || "")}"></label><label>To (comma-separated)<input name="to" value="${esc((c.email?.to || []).join(", "))}"></label></div>
   <div class="row3"><label>Daily token budget<input name="dtok" type="number" min="0" value="${c.budgets?.daily_tokens || ""}"></label><label>Daily cost budget (USD)<input name="dcost" type="number" min="0" step="0.01" value="${c.budgets?.daily_cost_usd || ""}"></label>
   <label>Warn at % of a task's budget<input name="tpct" type="number" min="-1" max="100" value="${c.budgets?.task_percent || ""}" placeholder="80"></label></div>
   <label>Dashboard URL for links<input name="url" value="${esc(c.dashboard_url || "")}" placeholder="https://spine.example.com"></label>
   <div class="err" id="nfErr"></div><div class="actions"><button class="btn primary">Save</button><button type="button" class="btn" id="ntest">Send a test</button></div></form>`;
  const f = $("#nf");
  const cfg = () => {
    const hooks = lines(f.hooks.value.replace(/,/g, "\n")).map(l => { const [url, fmt] = l.split(/\s+/); return { url, format: fmt === "generic" ? "generic" : "" } });
    const out = { desktop: f.desktop.checked, webhooks: hooks, events: $$("input[name=ev]", f).filter(x => x.checked).map(x => x.value),
      budgets: { daily_tokens: +f.dtok.value || 0, daily_cost_usd: +f.dcost.value || 0, task_percent: +f.tpct.value || 0 }, dashboard_url: f.url.value };
    if (f.host.value) out.email = { host: f.host.value, port: +f.port.value || 0, username: f.user.value, password_env: f.penv.value, from: f.from.value, to: lines(f.to.value) };
    return out;
  };
  f.onsubmit = async e => { e.preventDefault(); $("#nfErr").textContent = ""; try { await send("PUT", "/api/notify/config", cfg()); toast("Saved", "ok") } catch (x) { $("#nfErr").textContent = x.message } };
  $("#ntest").onclick = async () => { try { await send("PUT", "/api/notify/config", cfg()); const r = await send("POST", "/api/notify/test"); toast(r.errors?.length ? "Some deliveries failed:<br>" + r.errors.map(esc).join("<br>") : "Test sent", r.errors?.length ? "bad" : "ok") } catch (x) { $("#nfErr").textContent = x.message } };
}
async function settingsTemplates(rn) {
  const t = await get(`/api/repos/${enc(rn)}/templates`).catch(e => ({ error: e.message, templates: [] }));
  $("#tpl").className = "";
  $("#tpl").innerHTML = `${(t.errors || []).map(e => `<p class="fail">${esc(e)}</p>`).join("")}${t.templates.length ? `<table><tr><th>Name</th><th>Source</th><th>Scope</th><th>Gates</th><th>Agent</th><th></th></tr>${t.templates.map(x => `<tr><td><b>${esc(x.name)}</b><div class="muted">${esc(x.description || "")}</div></td><td>${x.source === "repo" ? "repository" : "dashboard"}</td>
    <td class="mono">${esc((x.scope || []).join(", ") || "—")}</td><td>${esc((x.gates || []).join(", ") || "all")}</td><td>${esc(x.agent || "default")}</td><td>${x.source === "user" && can("operator") ? `<button class="btn small danger" data-tdel="${esc(x.name)}">Delete</button>` : ""}</td></tr>`).join("")}</table>` : `<p class="muted">No templates.</p>`}
   <p class="hint">Create one from the New task form (“Save as template”). Repository templates live in <code>.spine/templates.yaml</code> and are shared through git.</p>`;
  $$("[data-tdel]").forEach(b => b.onclick = async () => { if (!confirm("Delete template " + b.dataset.tdel + "?")) return; await send("DELETE", "/api/templates/" + enc(b.dataset.tdel)); settingsTemplates(rn) });
}

boot();
