"use strict";

// Everything the model wrote goes into the page with textContent, never as
// HTML: the photos are read by a model and its words are not trusted.

const $ = (id) => document.getElementById(id);
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
const DAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const BY_KINDS = new Set(["sign", "return", "pay", "rsvp"]);

const state = {
  papers: [],
  week: null,
  health: null,
  editing: null,       // data-key of the field being edited
  confirm: null,       // paper id waiting for "Delete this paper?"
  editMode: new Set(), // paper ids showing Remove and Add
  showPile: false,
  earlierOpen: false,
  stale: false,        // new data arrived while a field was open
};
let pollTimer = 0;
let healthTimer = 0;
let loadSeq = 0;
// While a mouse button is down the page is not rebuilt, or the click would
// land on an element that no longer exists.
let pointerDown = false;
let renderPending = false;

// h builds an element. Strings become text nodes, never HTML.
function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === null || v === undefined || v === false) continue;
    if (k === "class") el.className = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "checked") el.checked = true;
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const kid of kids.flat()) {
    if (kid === null || kid === undefined || kid === false) continue;
    el.append(kid instanceof Node ? kid : String(kid));
  }
  return el;
}

function icon() {
  return $("calendar-icon").content.firstElementChild.cloneNode(true);
}

// ---- dates and times, shown the way a parent writes them ----

function day(iso) {
  const [y, m, d] = iso.split("-").map(Number);
  return new Date(y, m - 1, d);
}
function fmtDay(iso) {
  const d = day(iso);
  return `${DAYS[d.getDay()]}, ${MONTHS[d.getMonth()]} ${d.getDate()}`;
}
function fmtDays(from, to) {
  if (!to) return fmtDay(from);
  const a = day(from), b = day(to);
  if (a.getMonth() === b.getMonth()) return `${MONTHS[a.getMonth()]} ${a.getDate()}–${b.getDate()}`;
  return `${MONTHS[a.getMonth()]} ${a.getDate()} – ${MONTHS[b.getMonth()]} ${b.getDate()}`;
}
function clock(t) {
  let [hh, mm] = t.split(":").map(Number);
  const ap = hh < 12 ? "AM" : "PM";
  hh = hh % 12 || 12;
  return { text: mm ? `${hh}:${String(mm).padStart(2, "0")}` : `${hh}`, ap };
}
function fmtTimes(start, end) {
  if (!start) return "";
  const a = clock(start);
  if (!end) return `${a.text} ${a.ap}`;
  const b = clock(end);
  return a.ap === b.ap ? `${a.text}–${b.text} ${b.ap}` : `${a.text} ${a.ap}–${b.text} ${b.ap}`;
}
function plural(n, one, many) {
  return `${n} ${n === 1 ? one : many}`;
}

// ---- talking to the server ----

async function api(method, path, body) {
  const opts = { method, headers: {} };
  if (body instanceof FormData) {
    opts.body = body;
  } else if (body !== undefined) {
    opts.body = JSON.stringify(body);
    opts.headers["Content-Type"] = "application/json";
  }
  let res;
  try {
    res = await fetch(path, opts);
  } catch {
    throw new Error(STOPPED);
  }
  let data = null;
  if (res.status !== 204) {
    try { data = await res.json(); } catch { data = null; }
  }
  if (!res.ok) {
    const err = new Error((data && data.error) || "That did not work. Try again.");
    err.data = data;
    throw err;
  }
  return data;
}

function say(text) {
  $("status").textContent = text;
}

function waiting() {
  return state.papers.some((p) => p.status === "queued" || p.status === "reading");
}

const STOPPED = "Backpack has stopped. Start it again with: backpack";

async function load() {
  clearTimeout(pollTimer);
  const seq = ++loadSeq;
  try {
    const data = await api("GET", "/api/papers");
    if (seq !== loadSeq) return; // a newer answer is on its way
    state.papers = data.papers;
    state.week = data.week;
    if ($("status").textContent === STOPPED) say("");
  } catch (e) {
    say(e.message);
  }
  if (state.editing) state.stale = true;
  else render();
  if (waiting()) pollTimer = setTimeout(load, 1500);
  watchHealth();
}

async function checkHealth() {
  try {
    state.health = await api("GET", "/api/health");
  } catch (e) {
    state.health = { problem: e.message, reachable: false, backpackDown: true };
  }
  if (!state.editing) render();
  watchHealth();
}

// The model can go away and come back; look again while it matters. A
// check already planned is left alone, so polling cannot keep pushing it
// back.
function watchHealth() {
  const problem = state.health && state.health.problem;
  if (!problem && !waiting()) {
    clearTimeout(healthTimer);
    healthTimer = 0;
    return;
  }
  if (healthTimer) return;
  healthTimer = setTimeout(() => { healthTimer = 0; checkHealth(); }, problem ? 3000 : 10000);
}

async function patch(id, body) {
  try {
    await api("PATCH", `/api/papers/${encodeURIComponent(id)}`, body);
    say("");
  } catch (e) {
    say(e.message);
  }
  await load();
}
const patchEvent = (id, index, fields) => patch(id, { event: { index, ...fields } });
const patchAction = (id, index, fields) => patch(id, { action: { index, ...fields } });

async function upload(files) {
  const list = [...files];
  if (list.length === 0) return;
  const form = new FormData();
  for (const f of list) form.append("photos", f, f.name);
  showProblems([]);
  say(`Adding ${plural(list.length, "photo", "photos")}…`);
  try {
    const out = await api("POST", "/api/papers", form);
    showProblems(out.problems);
    const left = out.problems.length ? ` ${plural(out.problems.length, "file was", "files were")} not added; see above.` : "";
    say(`Added ${plural(out.papers.length, "photo", "photos")}. Reading them now.${left}`);
  } catch (e) {
    const problems = (e.data && e.data.problems) || [];
    showProblems(problems);
    say(problems.length ? `Nothing was added; see above.` : e.message);
  }
  await load();
}

function showProblems(problems) {
  $("problems").replaceChildren(...problems.map((p) => h("li", {}, p.file ? `${p.file}: ${p.error}` : p.error)));
}

// ---- editing in place ----

// editable shows text as a button; pressed, it turns into an input. Enter
// or leaving the field saves, Escape puts the old value back. With confirm
// set (a guessed date), Enter on the unchanged value saves it too, which
// tells Backpack a person checked it.
function editable(key, text, opts) {
  const { type = "text", value = "", label, onSave, cls = "", confirm = false } = opts;
  const button = () => h("button", {
    type: "button", class: `edit ${cls}`.trim(), "data-key": key,
    "aria-describedby": "edit-hint",
    onclick: () => { state.editing = key; render(); },
  }, text);
  if (state.editing !== key) return button();

  const input = h("input", { type, value, "aria-label": label, "data-key": key });
  let closed = false;
  const close = (save, enter) => {
    if (closed) return;
    closed = true;
    state.editing = null;
    const v = input.value.trim();
    // onSave returns the save in flight, or nothing when it refused the value.
    if (save && (v !== value || (enter && confirm)) && onSave(v)) return;
    // Nothing to save: put the button back in place without rebuilding the
    // page, so a click or Tab on the way out reaches where it was going.
    const b = button();
    input.replaceWith(b);
    if (enter !== undefined) b.focus();
    if (state.stale) render();
  };
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter") { e.preventDefault(); close(true, true); }
    if (e.key === "Escape") { e.preventDefault(); close(false, false); }
  });
  input.addEventListener("blur", () => close(true));
  return input;
}

function checkTag(confidence) {
  return confidence === "guessed" ? h("span", { class: "tag-check" }, "check") : null;
}

function dateButton(prefix, ev) {
  const key = `${prefix}:${ev.paper_id}:e${ev.index}:date`;
  const open = state.editing === key;
  const guessed = ev.date_confidence === "guessed";
  const text = ev.date ? fmtDays(ev.date, ev.end_date) : ev.raw || "Add a date";
  return [
    editable(key, text, {
      type: "date", value: ev.date || "", label: `Date for ${ev.title}`,
      cls: guessed ? "guess" : "", confirm: guessed,
      onSave: (v) => patchEvent(ev.paper_id, ev.index, { date: v }),
    }),
    open ? null : checkTag(ev.date_confidence),
  ];
}

function timeButton(prefix, ev, always) {
  if (!ev.start_time && !always) return null;
  const key = `${prefix}:${ev.paper_id}:e${ev.index}:time`;
  return editable(key, ev.start_time ? fmtTimes(ev.start_time, ev.end_time) : "add a time", {
    type: "time", value: ev.start_time || "", label: `Start time for ${ev.title}`,
    cls: ev.start_time ? "time" : "time empty",
    onSave: (v) => patchEvent(ev.paper_id, ev.index, { start_time: v }),
  });
}

function titleButton(prefix, ev) {
  return editable(`${prefix}:${ev.paper_id}:e${ev.index}:title`, ev.title, {
    value: ev.title, label: "What happens",
    onSave: (v) => v && patchEvent(ev.paper_id, ev.index, { title: v }),
  });
}

function dueButton(prefix, t, always) {
  if (!t.due && !t.raw && !always) return null;
  const key = `${prefix}:${t.paper_id}:a${t.index}:due`;
  const by = BY_KINDS.has(t.kind) ? "by " : "";
  const text = t.due ? by + fmtDay(t.due) : t.raw || "add a date";
  return h("span", { class: "due" },
    editable(key, text, {
      type: "date", value: t.due || "", label: `Date for ${t.text}`, confirm: t.date_confidence === "guessed",
      cls: [t.date_confidence === "guessed" ? "guess" : "", t.due || t.raw ? "" : "empty"].join(" "),
      onSave: (v) => patchAction(t.paper_id, t.index, { due: v }),
    }),
    checkTag(t.date_confidence));
}

function todoBox(prefix, t) {
  return h("input", {
    type: "checkbox", checked: t.done, "data-key": `${prefix}:${t.paper_id}:a${t.index}:done`,
    "aria-label": t.text,
    onchange: (e) => patchAction(t.paper_id, t.index, { done: e.target.checked }),
  });
}

// ---- the page ----

function render() {
  if (pointerDown) {
    renderPending = true;
    return;
  }
  const focused = document.activeElement && document.activeElement.dataset.key;
  state.stale = false;

  const problem = state.health && state.health.problem;
  renderBanner(problem);
  $("drop").hidden = Boolean(problem);
  $("first").hidden = state.papers.length > 0;
  $("week-label").textContent = state.week ? state.week.label : "";
  $("main").replaceChildren(...renderWeek(), ...renderPapers());
  // One primary button per screen: the calendar once there are dates,
  // until then the way to add photos.
  $("choose").classList.toggle("primary", !document.querySelector('[data-key="ics"]'));

  const again = focused && document.querySelector(`[data-key="${CSS.escape(focused)}"]`);
  const editing = state.editing && document.querySelector(`[data-key="${CSS.escape(state.editing)}"]`);
  if (editing) {
    editing.focus();
    if (editing.select) editing.select();
  } else if (again) {
    again.focus();
  }
  if (state.editing && document.activeElement !== editing) {
    // The field could not take focus (it is gone, or hidden): stop editing
    // rather than leave the page waiting for it.
    state.editing = null;
    render();
  }
}

document.addEventListener("pointerdown", () => { pointerDown = true; }, true);
for (const type of ["pointerup", "pointercancel"]) {
  document.addEventListener(type, () => setTimeout(() => {
    pointerDown = false;
    if (renderPending) {
      renderPending = false;
      render();
    }
  }, 0), true);
}

function renderBanner(problem) {
  const b = $("banner");
  b.hidden = !problem;
  // Same words as last time: leave the alert alone so it is not announced again.
  const sig = problem ? `${problem}|${state.health.model}|${state.health.reachable}` : "";
  if (b.dataset.sig === sig) return;
  b.dataset.sig = sig;
  if (!problem) return b.replaceChildren();
  const health = state.health;
  const model = health.model || "gemma4:e4b";
  const code = (t) => { const c = document.createElement("code"); c.textContent = t; return c; };
  const strong = (t) => { const s = document.createElement("strong"); s.textContent = t; return s; };
  if (health.backpackDown) {
    b.replaceChildren(strong("Backpack has stopped."), " Start it again: ", code("backpack"));
  } else if (health.reachable) {
    b.replaceChildren(strong("Backpack needs its model."), " Get it once: ", code(`ollama pull ${model}`));
  } else {
    b.replaceChildren(strong("Backpack can't reach the model."), " Start Ollama: ", code("ollama serve"),
      " · first time only: ", code(`ollama pull ${model}`));
  }
}

function renderWeek() {
  const w = state.week;
  if (!w) return [];
  const out = [];

  if (w.dates.length || w.earlier.length) {
    const inFile = w.dates.filter((d) => d.date && d.include !== false).length;
    out.push(h("h2", {}, "Dates"), h("div", { class: "block" },
      h("div", { class: "head" },
        h("h3", {}, plural(w.dates.length, "date", "dates")),
        inFile ? h("a", { class: "button primary", href: "/api/calendar.ics", download: "backpack.ics", "data-key": "ics" }, "Download calendar (.ics)") : null),
      w.dates.map((d) => dateRow(d)),
      w.earlier.length ? h("details", {
        class: "earlier", open: state.earlierOpen,
        ontoggle: (e) => { state.earlierOpen = e.target.open; },
      },
        h("summary", { "data-key": "earlier" }, `Earlier (${w.earlier.length})`),
        w.earlier.map((d) => dateRow(d))) : null));
  }

  if (w.todos.length) {
    out.push(h("h2", {}, "To-do"), h("div", { class: "block" },
      h("div", { class: "head" },
        h("h3", {}, `${w.open} open`),
        w.open ? h("button", { type: "button", class: "quiet", "data-key": "copy", onclick: copyList }, "Copy list") : null),
      w.todos.map((t) => h("div", { class: `todo${t.done ? " done" : ""}` },
        h("label", {}, todoBox("w", t), h("span", {}, t.text)),
        t.done ? h("span", { class: "due" }, "done") : dueButton("w", t)))));
  }
  return out;
}

function dateRow(d) {
  const off = d.include === false;
  return h("div", { class: `row${off ? " off" : ""}` },
    h("span", { class: "date" }, dateButton("w", d), " ", timeButton("w", d)),
    h("span", { class: "what" }, titleButton("w", d),
      d.location ? h("span", { class: "loc" }, ` · ${d.location}`) : null,
      off ? h("span", { class: "visually-hidden" }, " (not in the calendar file)") : null),
    h("span", { class: "from" }, d.paper));
}

async function copyList() {
  const lines = state.week.todos.filter((t) => !t.done).map((t) => {
    const by = BY_KINDS.has(t.kind) ? "by " : "";
    return `- ${t.text}${t.due ? ` (${by}${fmtDay(t.due)})` : ""}`;
  });
  const text = lines.join("\n");
  try {
    await navigator.clipboard.writeText(text);
    say("Copied the list.");
    return;
  } catch {
    // Some browsers keep the clipboard API to themselves; the old way still works.
  }
  const area = h("textarea", { class: "visually-hidden", readonly: true });
  area.value = text;
  document.body.append(area);
  area.select();
  const ok = document.execCommand("copy");
  area.remove();
  focusKey("copy");
  say(ok ? "Copied the list." : "The browser did not allow copying. Select the list and copy it by hand.");
}

function renderPapers() {
  if (state.papers.length === 0) return [];
  const pile = state.papers.filter((p) => p.status === "done" && p.reading.nothing_to_do);
  // What is happening now goes on top: the paper being read, then the line
  // in the order it will be read, then the rest, newest first.
  const rank = { reading: 0, queued: 1 };
  const shown = state.papers
    .filter((p) => state.showPile || !pile.includes(p))
    .map((p, i) => ({ p, i }))
    .sort((a, b) => (rank[a.p.status] ?? 2) - (rank[b.p.status] ?? 2) ||
      (a.p.status === "queued" ? b.i - a.i : a.i - b.i))
    .map((x) => x.p);
  const out = [h("h2", { tabindex: "-1", "data-key": "papers" }, "Papers")];
  if (shown.length) out.push(h("div", { class: "cards" }, shown.map(card)));
  if (pile.length) {
    out.push(h("p", { class: "pile" },
      state.showPile ? "" : `${plural(pile.length, "paper needs", "papers need")} nothing from you.`,
      h("button", {
        type: "button", class: "quiet", "data-key": "pile", "aria-expanded": String(state.showPile),
        onclick: () => { state.showPile = !state.showPile; render(); },
      }, state.showPile ? "Hide the papers that need nothing" : "Show")));
  }
  return out;
}

function averageSeconds() {
  const times = state.papers.filter((p) => p.read_ms > 0).map((p) => p.read_ms);
  if (times.length === 0) return 0;
  const avg = times.reduce((a, b) => a + b, 0) / times.length / 1000;
  return Math.max(5, Math.round(avg / 5) * 5);
}

function card(p) {
  const thumb = h("a", {
    class: "thumb", href: `/api/papers/${encodeURIComponent(p.id)}/photo`, target: "_blank", rel: "noopener",
    "aria-label": "Open the photo", "data-key": `c:${p.id}:photo`,
  }, h("img", { src: `/api/papers/${encodeURIComponent(p.id)}/photo`, alt: "", loading: "lazy" }));
  return h("article", { class: "card", "aria-label": p.status === "done" ? p.reading.title : "Paper" },
    thumb, h("div", {}, ...cardBody(p)));
}

function cardBody(p) {
  if (p.status === "queued" || p.status === "reading") {
    const secs = averageSeconds();
    const hint = p.status === "queued" ? "Photos are read one at a time."
      : secs ? `About ${secs} seconds on this laptop.` : "The first one takes a little longer.";
    return [
      h("h3", {}, p.status === "queued" ? "Waiting its turn" : "Reading…"),
      h("p", { class: "sum status" }, p.status === "reading" ? h("span", { class: "spin", "aria-hidden": "true" }) : null, hint),
      tools(p, []),
    ];
  }
  if (p.status === "failed") {
    return [
      h("h3", { class: "failed" }, "Couldn't read this one"),
      h("p", { class: "sum" }, p.error || "The photo may be too dark or blurry. Try a brighter shot, or read it yourself."),
      tools(p, [h("button", {
        type: "button", "data-key": `c:${p.id}:reread`,
        onclick: async () => {
          try { await api("POST", `/api/papers/${encodeURIComponent(p.id)}/reread`); } catch (e) { say(e.message); }
          await load();
          focusKey(`c:${p.id}:delete`);
        },
      }, "Read again")]),
    ];
  }

  const r = p.reading;
  const editing = state.editMode.has(p.id);
  const items = [];
  r.events.forEach((e, index) => {
    const ev = { ...e, index, paper_id: p.id };
    const on = e.include !== false;
    items.push(h("li", {},
      h("button", {
        type: "button", class: "cal", "aria-pressed": String(on), "data-key": `c:${p.id}:e${index}:cal`,
        "aria-label": `${e.title}: in the calendar file`,
        title: on ? "In the calendar file" : "Left out of the calendar file",
        onclick: () => patchEvent(p.id, index, { include: !on }),
      }, icon()),
      h("span", {}, dateButton("c", ev), " ", timeButton("c", ev, editing)),
      h("span", {}, "— ", titleButton("c", ev)),
      editing ? removeButton(p, `c:${p.id}:e${index}:remove`, `Remove ${e.title}`, { event: { index, remove: true } }) : null));
  });
  r.actions.forEach((a, index) => {
    const t = { ...a, index, paper_id: p.id };
    items.push(h("li", {},
      todoBox("c", t),
      editable(`c:${p.id}:a${index}:text`, a.text, {
        value: a.text, label: "What to do",
        onSave: (v) => v && patchAction(p.id, index, { text: v }),
      }),
      dueButton("c", t, editing),
      editing ? removeButton(p, `c:${p.id}:a${index}:remove`, `Remove ${a.text}`, { action: { index, remove: true } }) : null));
  });

  const extra = [];
  if (editing) {
    extra.push(
      h("button", { type: "button", class: "quiet", "data-key": `c:${p.id}:add-date`, onclick: () => addEvent(p) }, "Add a date"),
      h("button", { type: "button", class: "quiet", "data-key": `c:${p.id}:add-todo`, onclick: () => addAction(p) }, "Add a to-do"));
  }
  if (r.events.length || r.actions.length || editing) {
    extra.push(h("button", {
      type: "button", class: "quiet", "data-key": `c:${p.id}:editmode`,
      onclick: () => { editing ? state.editMode.delete(p.id) : state.editMode.add(p.id); render(); },
    }, editing ? "Done editing" : "Edit"));
  }

  return [
    h("h3", {}, editable(`c:${p.id}:title`, r.title, {
      value: r.title, label: "Name of this paper", onSave: (v) => v && patch(p.id, { title: v }),
    })),
    r.summary ? h("p", { class: "sum" }, r.summary) : null,
    items.length ? h("ul", {}, items) : null,
    r.nothing_to_do ? h("p", { class: "nothing" }, h("b", {}, "Nothing to do."), " Safe to toss.") : null,
    tools(p, extra),
  ];
}

// removeButton takes a line off a paper; focus goes to the card's
// "Done editing" so the keyboard does not fall back to the top.
function removeButton(p, key, label, body) {
  return h("button", {
    type: "button", class: "quiet remove danger", "data-key": key, "aria-label": label,
    onclick: async () => { await patch(p.id, body); focusKey(`c:${p.id}:editmode`); },
  }, "Remove");
}

async function addEvent(p) {
  const d = new Date();
  const today = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
  await patch(p.id, { event: { title: p.reading.title, date: today } });
  const fresh = state.papers.find((x) => x.id === p.id);
  if (fresh && fresh.reading.events.length > p.reading.events.length) {
    state.editing = `c:${p.id}:e${fresh.reading.events.length - 1}:date`;
    render();
  }
}

async function addAction(p) {
  await patch(p.id, { action: { text: "New to-do" } });
  const fresh = state.papers.find((x) => x.id === p.id);
  if (fresh && fresh.reading.actions.length > p.reading.actions.length) {
    state.editing = `c:${p.id}:a${fresh.reading.actions.length - 1}:text`;
    render();
  }
}

function tools(p, extra) {
  if (state.confirm === p.id) {
    const cancel = () => { state.confirm = null; render(); focusKey(`c:${p.id}:delete`); };
    return h("div", { class: "tools", onkeydown: (e) => { if (e.key === "Escape") cancel(); } },
      h("span", { class: "confirm" }, "Delete this paper? Its photo is removed too."),
      h("button", {
        type: "button", class: "danger", "data-key": `c:${p.id}:yes`,
        onclick: async () => {
          state.confirm = null;
          try { await api("DELETE", `/api/papers/${encodeURIComponent(p.id)}`); say("Deleted."); } catch (e) { say(e.message); }
          await load();
          if (!focusKey("papers") && !$("drop").hidden) $("choose").focus();
        },
      }, "Yes, delete"),
      h("button", {
        type: "button", "data-key": `c:${p.id}:no`,
        onclick: cancel,
      }, "No"));
  }
  return h("div", { class: "tools" }, ...extra,
    h("button", {
      type: "button", class: "quiet", "data-key": `c:${p.id}:delete`,
      onclick: () => { state.confirm = p.id; render(); focusKey(`c:${p.id}:no`); },
    }, "Delete"));
}

function focusKey(key) {
  const el = document.querySelector(`[data-key="${CSS.escape(key)}"]`);
  if (el) el.focus();
  return Boolean(el);
}

// ---- dropping photos ----

function setupDrop() {
  const drop = $("drop");
  let depth = 0;
  document.addEventListener("dragenter", (e) => {
    if (![...e.dataTransfer.types].includes("Files")) return;
    depth++;
    drop.classList.add("over");
  });
  document.addEventListener("dragleave", () => {
    if (--depth <= 0) { depth = 0; drop.classList.remove("over"); }
  });
  document.addEventListener("dragover", (e) => e.preventDefault());
  document.addEventListener("drop", (e) => {
    e.preventDefault();
    depth = 0;
    drop.classList.remove("over");
    if (e.dataTransfer.files.length) upload(e.dataTransfer.files);
  });
  $("choose").addEventListener("click", () => $("file").click());
  $("file").addEventListener("change", (e) => {
    upload(e.target.files);
    e.target.value = "";
  });
}

setupDrop();
checkHealth();
load();
// Coming back to the tab shows what changed meanwhile.
window.addEventListener("focus", () => { if (!state.editing) load(); });
