// Status page: the public page behind /status/<token> loads /api/status/<token> every minute
// and renders it into #sp. app.js reuses PK95Status.render for the preview on "Statusseite".
"use strict";
(function () {
  const ICONS = {
    check: "M4.5 12.75l6 6 9-13.5",
    warn: "M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126zM12 15.75h.007v.008H12v-.008z",
    info: "M11.25 11.25l.041-.02a.75.75 0 011.063.852l-.708 2.836a.75.75 0 001.063.853l.041-.021M21 12a9 9 0 11-18 0 9 9 0 0118 0zm-9-3.75h.008v.008H12V8.25z",
    wrench: "M21.75 6.75a4.5 4.5 0 01-4.884 4.484c-1.076-.091-2.264.071-2.95.904l-7.152 8.684a2.548 2.548 0 11-3.586-3.586l8.684-7.152c.833-.686.995-1.874.904-2.95a4.5 4.5 0 016.336-4.486l-3.276 3.276a3.004 3.004 0 002.25 2.25l3.276-3.276c.256.565.398 1.192.398 1.852z",
  };
  const icon = (name) => `<svg class="icon" viewBox="0 0 24 24" aria-hidden="true"><path d="${ICONS[name]}"/></svg>`;
  const esc = (v) => String(v ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const p2 = (x) => String(x).padStart(2, "0");
  const WD = ["So", "Mo", "Di", "Mi", "Do", "Fr", "Sa"];
  const NNBSP = " ";

  const dateText = (d, year) => `${p2(d.getDate())}.${p2(d.getMonth() + 1)}.${year ? d.getFullYear() : ""}`;
  const clock = (d) => `${p2(d.getHours())}:${p2(d.getMinutes())}`;
  const sameDay = (a, b) => a.toDateString() === b.toDateString();
  const fmtTime = (ts) => { const d = new Date(ts * 1000); return `${dateText(d, true)}, ${clock(d)} Uhr`; };
  // "14:05 Uhr" today, else "13.11., 22:10 Uhr"
  function fmtShort(ts) {
    const d = new Date(ts * 1000);
    return sameDay(d, new Date()) ? `${clock(d)} Uhr` : `${dateText(d)}, ${clock(d)} Uhr`;
  }
  // "Sa, 15.11. 22:00–02:00 Uhr" (an end on the next day is clear from the times)
  function fmtRange(s, e) {
    const a = new Date(s * 1000), b = new Date(e * 1000);
    const head = `${WD[a.getDay()]}, ${dateText(a, a.getFullYear() !== new Date().getFullYear())}`;
    if (sameDay(a, b) || (e - s < 86400 && b.getHours() * 60 + b.getMinutes() <= a.getHours() * 60 + a.getMinutes())) return `${head} ${clock(a)}–${clock(b)} Uhr`;
    return `${head} ${clock(a)} Uhr bis ${WD[b.getDay()]}, ${dateText(b)} ${clock(b)} Uhr`;
  }
  function fmtDur(s) {
    if (s < 3600) return `${Math.max(1, Math.round(s / 60))}${NNBSP}Min.`;
    if (s < 86400) return `${(s / 3600).toFixed(1).replace(".", ",")}${NNBSP}Std.`;
    const d = Math.round(s / 86400);
    return `${d} ${d === 1 ? "Tag" : "Tage"}`;
  }
  const dayLabel = (iso) => { const [y, m, d] = String(iso).split("-"); return `${d}.${m}.${y}`; };
  const pct = (v) => (v >= 99.995 ? "100" : v.toFixed(2).replace(".", ",")) + NNBSP + "%";

  const BANNER_ICON = { ok: "check", warn: "warn", crit: "warn", maint: "wrench", unknown: "info" };
  const NOTICE = { info: "Hinweis", warn: "Einschränkung", crit: "Störung" };

  function bars(c, days) {
    let crit = 0, warn = 0;
    const cells = (c.history || []).map((d) => {
      let t = dayLabel(d.day) + ": ";
      if (d.state === "none") t += "keine Daten";
      else if (d.state === "crit") { crit++; t += `Störung, ${fmtDur(d.down)}`; }
      else if (d.state === "warn") { warn++; t += `eingeschränkt, ${fmtDur(d.limited)}`; }
      else t += "ohne Störung";
      return `<span class="stp-bar ${esc(d.state)}" title="${esc(t)}"></span>`;
    }).join("");
    const label = `Verlauf der letzten ${days} Tage: ${crit ? `${crit} ${crit === 1 ? "Tag" : "Tage"} mit Störung` : "keine Störung"}${warn ? `, ${warn} eingeschränkt` : ""}`;
    return `<div class="stp-bars" role="img" aria-label="${esc(label)}">${cells}</div>`;
  }

  function component(c, days, preview) {
    const sub = c.since ? `seit ${fmtShort(c.since)}` : c.until ? `bis ${fmtShort(c.until)}` : "";
    return `<section class="stp-comp">
      <div class="stp-comp-head">
        <div class="grow"><h3 class="stp-name">${esc(c.name)}</h3>${c.note ? `<div class="stp-note">${esc(c.note)}</div>` : ""}
          ${preview ? `<div class="stp-note">${c.members ? `${c.members} ${c.members === 1 ? "Gerät" : "Geräte"}${c.units ? ` · ${c.units} ${c.units === 1 ? "Check" : "Checks"}` : ""}` : "Keine aktiven Geräte"}</div>` : ""}</div>
        <div class="stp-state ${esc(c.state)}"><span class="stp-dot"></span>${esc(c.text)}${sub ? `<span class="stp-since">${esc(sub)}</span>` : ""}</div>
      </div>
      ${bars(c, days)}
      <div class="stp-axis"><span>vor ${days} Tagen</span><span>${c.availability != null ? `${pct(c.availability)} verfügbar` : ""}</span><span>Heute</span></div>
    </section>`;
  }

  function maintenance(list) {
    if (!list || !list.length) return "";
    return `<h2 class="stp-sec">Wartungsarbeiten</h2><div class="stp-card">${list.map((m) => `<div class="stp-mt">
      <div class="stp-mt-head"><b>${esc(m.name)}</b>${m.running ? `<span class="pill blue pulse"><span class="d"></span>Läuft</span>` : `<span class="pill">Geplant</span>`}</div>
      <div class="stp-mt-time">${icon("wrench")}${esc(fmtRange(m.start, m.end))}</div>
      ${m.note ? `<div class="stp-note">${esc(m.note)}</div>` : ""}
      ${m.components && m.components.length ? `<div class="stp-note">Betrifft: ${m.components.map(esc).join(", ")}</div>` : ""}
    </div>`).join("")}</div>`;
  }

  // render draws status s into el. opts.preview adds the device counts and leaves out the
  // title and footer; opts.stale marks data that could not be refreshed.
  function render(el, s, opts = {}) {
    const days = s.days || 30;
    el.innerHTML = `${opts.preview ? "" : `<header class="stp-head"><h1 class="stp-title">${esc(s.title || "Systemstatus")}</h1>${s.intro ? `<p class="stp-intro">${esc(s.intro)}</p>` : ""}</header>`}
      <div class="stp-overall ${esc(s.state)}" role="status">${icon(BANNER_ICON[s.state] || "info")}<span>${esc(s.text)}</span></div>
      ${s.notice ? `<div class="stp-notice ${esc(s.notice.level)}"><div class="stp-notice-head">${icon(s.notice.level === "info" ? "info" : "warn")}<b>${NOTICE[s.notice.level] || "Hinweis"}</b>${s.notice.at ? `<span class="stp-since">${esc(fmtTime(s.notice.at))}</span>` : ""}</div><p>${esc(s.notice.text)}</p></div>` : ""}
      ${(s.components || []).length ? `<h2 class="stp-sec">Dienste</h2><div class="stp-card">${s.components.map((c) => component(c, days, opts.preview)).join("")}</div>` : ""}
      ${maintenance(s.maintenance)}
      ${opts.preview ? "" : `<footer class="stp-foot">Stand: ${esc(fmtTime(s.updated))} · aktualisiert sich jede Minute${opts.stale ? " · <b>Verbindung unterbrochen – zeigt den letzten Stand</b>" : ""}</footer>`}`;
  }

  window.PK95Status = { render };

  // ---- the public page ----
  const root = document.getElementById("stp");
  if (!root) return;
  const token = location.pathname.split("/").filter(Boolean)[1] || "";
  let last = null, timer = null;
  const message = (title, text) => `<div class="stp-empty">${icon("info")}<h1 class="stp-title">${esc(title)}</h1><p class="stp-intro">${esc(text)}</p></div>`;
  async function load() {
    let stale = false;
    try {
      const r = await fetch(`/api/status/${encodeURIComponent(token)}`, { cache: "no-store", headers: { Accept: "application/json" } });
      if (r.status === 404) {
        clearInterval(timer);
        document.title = "Statusseite nicht gefunden";
        root.innerHTML = message("Diese Statusseite gibt es nicht", "Der Link ist falsch oder nicht mehr gültig, oder die Statusseite ist ausgeschaltet.");
        return;
      }
      if (!r.ok) throw new Error(String(r.status));
      last = await r.json();
    } catch (e) {
      stale = true;
      if (!last) {
        root.innerHTML = message("Statusseite nicht erreichbar", "Bitte in ein paar Minuten erneut versuchen.");
        return;
      }
    }
    document.title = `${last.title ? last.title + " – " : ""}Systemstatus`;
    render(root, last, { stale });
  }
  load();
  timer = setInterval(load, 60000);
  document.addEventListener("visibilitychange", () => { if (!document.hidden && last) load(); });
})();
