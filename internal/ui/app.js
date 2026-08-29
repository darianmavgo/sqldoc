"use strict";
(() => {
const CFG = window.__SQLDOC__;
const BLOCK = 200;              // rows per fetched window
const OVERSCAN = 8;             // rows rendered beyond the viewport
const MAX_SIZER = 15000000;     // px; beyond this browsers stop scrolling accurately
const BASE_ROW_H = 28;

const $ = (s) => document.querySelector(s);
const el = { bar:$("#bar"), sel:$("#tbl"), meta:$("#meta"), scroll:$("#scroll"),
             sizer:$("#sizer"), rows:$("#rows"), head:$("#head"), status:$("#status"),
             pos:$("#pos"), timing:$("#timing"), path:$("#path"), empty:$("#empty"),
             err:$("#err"), find:$("#find"), findIn:$("#findq"), findCount:$("#findcount"),
             findProg:$("#findprog"),
             docs:$("#docs"), start:$("#start"), recents:$("#recents"),
             drop:$("#drop"), dropMsg:$("#dropmsg"), busy:$("#busy"),
             busyFill:$("#busyfill"), busyMsg:$("#busymsg"),
             pathInput:$("#pathinput"), startFoot:$("#startfoot"),
             startOpen:$("#startopen"), closeBtn:$("#closebtn"),
             doc:$("#doc"), gallery:$("#gallery"), viewBtn:$("#viewbtn") };

const S = {
  session:{docs:[], recents:[], canPick:false}, docId:null,
  doc:null, table:null, cols:[], count:{known:false,rows:0,exact:false},
  hasRowid:false, top:0, blocks:new Map(), pending:new Map(), widths:[], gutter:64,
  view:"table", galleryLoaded:false,
  sort:null, zoom:1, scale:1, viewport:30, syncing:false, dirty:false, lastDir:1, prevTop:0, userSized:false, slack:null,
  find:{q:"",matches:[],idx:-1,cursor:0,done:true,running:false,restart:null,hits:new Map()},
};

const rowH = () => BASE_ROW_H * S.zoom;
// Every request carries the document it is about. Threading it through one
// place means no endpoint can accidentally answer about the wrong file when
// more than one is open.
const api = (p, q={}) => {
  const u = new URL(p, location.origin);
  u.searchParams.set("k", CFG.token);
  if (S.docId && !("doc" in q)) u.searchParams.set("doc", S.docId);
  for (const [k,v] of Object.entries(q)) if (v !== undefined && v !== null && v !== "") u.searchParams.set(k, v);
  return u.toString();
};
const getJSON = async (p, q, signal) => {
  const r = await fetch(api(p,q), {signal});
  const j = await r.json();
  if (!r.ok || j.error) throw new Error(j.error || r.statusText);
  return j;
};
const fmtInt = (n) => n.toLocaleString("en-US");
const fmtBytes = (n) => n < 1024 ? n+" B" : n < 1048576 ? (n/1024).toFixed(1)+" KB"
                      : n < 1073741824 ? (n/1048576).toFixed(1)+" MB" : (n/1073741824).toFixed(2)+" GB";

/* ---------------------------------------------------------------- boot */
async function boot() {
  wire();
  wireOpening();
  await refreshSession();
  const args = new URL(location.href).searchParams;

  const first = S.session.docs[0];
  if (first) { await openDoc(first.id); return; }

  // The native window creates itself before it opens anything, because opening
  // a file can block for as long as the operating system feels like it. So the
  // documents are still on their way, and the window says so rather than
  // showing a start page that is about to be replaced.
  if (args.has("opening")) { waitForDocs(+args.get("opening") || 1); return; }

  // Nothing to show and nothing coming. The start page goes up first so that
  // dismissing the dialog lands somewhere, and so the dialog is a sheet on a
  // window that is already on screen rather than a panel floating on its own.
  showStart();
  if (S.session.canPick && args.has("pick")) await pickFile();
}

// waitForDocs holds the window on a progress message until the documents the
// viewer is opening have arrived.
//
// The viewer calls sqldocOpened when it is done, but it cannot be relied on to
// arrive after this page is listening: a database that opens quickly is open
// before the first script runs. So the session is polled as well, and whichever
// gets here first wins. The deadline is the answer to an open that never
// returns at all - the window says what happened instead of waiting forever.
function waitForDocs(n) {
  el.busy.classList.add("on");
  el.busyFill.style.width = "100%";
  el.busyFill.classList.add("waiting");
  el.busyMsg.textContent = n > 1 ? `Opening ${n} databases…` : "Opening…";

  let done = false;
  const finish = async () => {
    if (done) return;
    done = true;
    clearInterval(poll);
    el.busy.classList.remove("on");
    el.busyFill.classList.remove("waiting");
    el.busyFill.style.width = "0";
    await refreshSession();
    const first = S.session.docs[0];
    if (first) await openDoc(first.id);
    else showStart();
  };

  window.sqldocOpened = finish;
  const started = Date.now();
  const poll = setInterval(async () => {
    if (done) return;
    await refreshSession();
    if (S.session.docs.length || Date.now() - started > 60000) finish();
  }, 300);
}

async function refreshSession() {
  try { S.session = await getJSON("/api/session", {doc: null}); }
  catch { S.session = {docs:[], recents:[], canPick:false}; }
  renderDocBar();
  renderStart();
}

// openDoc switches the viewer to one of the open documents.
async function openDoc(id) {
  S.docId = id;
  el.start.classList.remove("on");
  el.err.classList.remove("on");

  try {
    S.doc = await getJSON("/api/doc");
  } catch (e) { return fail(e); }

  document.title = (S.doc.style.title || S.doc.name) + " — sqldoc";
  if (S.doc.style.theme === "light" || S.doc.style.theme === "dark")
    document.documentElement.dataset.theme = S.doc.style.theme;
  if (S.doc.style.accent) document.documentElement.style.setProperty("--accent", S.doc.style.accent);
  updateStatusPath();

  const visible = S.doc.tables.filter(t => !t.hidden);
  const list = visible.length ? visible : S.doc.tables;
  if (!list.length) {
    el.empty.classList.add("on");
    el.empty.textContent = "This database has no tables.";
    renderDocBar();
    return;
  }

  el.sel.innerHTML = "";
  for (const t of list) {
    const o = document.createElement("option");
    o.value = t.name; o.textContent = t.label + (t.type === "view" ? "  (view)" : "");
    el.sel.appendChild(o);
  }
  el.sel.value = list[0].name;
  el.viewBtn.hidden = list.length < 2;
  renderDocBar();

  // A document switched to from another must not inherit the previous one's
  // idea of which table (or which view) was showing.
  S.table = null;
  S.galleryLoaded = false;
  if (S.doc.defaultView === "gallery") await showGalleryView();
  else await showTableView(list[0].name);
}

function fail(e) {
  el.err.classList.add("on");
  el.err.textContent = String(e && e.message || e);
}

// updateStatusPath re-elides the status bar's path to whatever space is
// actually available right now. Always computed from the pristine
// S.doc.path, never from whatever text happens to be on screen already, so
// repeated resizes narrow and widen the same way instead of compounding.
function updateStatusPath() {
  if (!S.doc) return;
  el.path.textContent = elidePath(S.doc.path, el.path.clientWidth, statusFont());
}

/* ------------------------------------------------- opening documents */
function renderDocBar() {
  const docs = S.session.docs || [];
  el.docs.innerHTML = "";
  for (const d of docs) {
    const o = document.createElement("option");
    o.value = d.id;
    o.textContent = d.name;
    o.title = d.path;
    el.docs.appendChild(o);
  }
  if (S.docId) el.docs.value = S.docId;
  el.docs.classList.toggle("solo", docs.length < 2);

  // The document switcher stays available on the start page so you can get
  // back to something already open; the per-table controls only make sense
  // once a document is actually showing.
  el.docs.style.display = docs.length ? "" : "none";
  el.closeBtn.style.display = S.docId ? "" : "none";
  for (const id of ["tbl","findbtn","zin","zout","save"]) {
    const n = document.getElementById(id);
    if (n) n.style.display = S.docId ? "" : "none";
  }
  el.docs.classList.toggle("unset", !S.docId);
  if (!S.docId) el.docs.selectedIndex = -1;
}

function showStart() {
  S.docId = null;
  S.doc = null;
  S.cols = [];
  S.table = null;
  S.view = "table";
  S.galleryLoaded = false;
  abortAll();
  document.title = "sqldoc";
  el.start.classList.add("on");
  el.doc.classList.remove("off");
  el.gallery.classList.remove("on");
  el.gallery.innerHTML = "";
  el.viewBtn.classList.remove("on");
  el.viewBtn.hidden = true;
  el.pos.textContent = "";
  el.meta.textContent = "";
  el.timing.textContent = "";
  el.path.textContent = "";
  renderDocBar();
  renderStart();
  el.pathInput.focus();
}

function renderStart() {
  const canPick = !!S.session.canPick;
  el.startOpen.hidden = !canPick;
  el.startFoot.textContent = S.session.driver
    ? `sqldoc ${S.session.version || ""} · ${S.session.driver}`.trim()
    : "";

  const rec = S.session.recents || [];
  el.recents.innerHTML = "";
  if (!rec.length) {
    const p = document.createElement("p");
    p.className = "startempty";
    p.textContent = canPick
      ? "Nothing opened yet."
      : "Nothing opened yet. Drop a database here, or paste its path above.";
    el.recents.appendChild(p);
    return;
  }
  const h = document.createElement("h2");
  h.textContent = "Recent";
  el.recents.appendChild(h);
  for (const r of rec) {
    const b = document.createElement("button");
    b.className = "recent";
    b.title = r.path;
    b.innerHTML = `<span class="rname">${esc(r.name)}</span>` +
                  `<span class="rpath"></span>` +
                  `<span class="rsize">${fmtBytes(r.size)}</span>`;
    b.onclick = () => openPath(r.path);
    el.recents.appendChild(b);
    const rp = b.querySelector(".rpath");
    rp.dataset.full = r.path;
    rp.textContent = elidePath(r.path, rp.clientWidth, recentPathFont());
  }
}

// openPath opens a database the server can reach by name. This is the cheap
// path: nothing is copied, however large the file is.
async function openPath(path) {
  if (!path) return;
  try {
    const d = await getJSON("/api/open", {path, doc: null});
    await refreshSession();
    await openDoc(d.id);
  } catch (e) { fail(e); }
}

// pickFile asks the server to show the operating system's own open dialog,
// because a browser will not tell a page where a chosen file lives.
//
// The request does not return until someone has chosen a file or dismissed the
// dialog, so the page says what it is waiting for. A button that silently does
// nothing for as long as a dialog stays open is indistinguishable from a broken
// one, which is exactly how this looked before.
async function pickFile() {
  el.busy.classList.add("on");
  el.busyFill.style.width = "100%";
  el.busyFill.classList.add("waiting");
  el.busyMsg.textContent = "Choose a database in the file dialog…";
  try {
    const d = await getJSON("/api/pick", {doc: null});
    if (d.cancelled) return;
    await refreshSession();
    await openDoc(d.id);
  } catch (e) {
    fail(e);
  } finally {
    el.busy.classList.remove("on");
    el.busyFill.classList.remove("waiting");
    el.busyFill.style.width = "0";
  }
}

async function closeDoc(id) {
  if (!id) return;
  try { await getJSON("/api/close", {doc: id}); } catch {}
  const wasCurrent = id === S.docId;
  S.docId = null;
  await refreshSession();
  const next = S.session.docs[0];
  if (next && wasCurrent) await openDoc(next.id);
  else if (!next) showStart();
  else await openDoc(S.session.docs.find(d => d.id === id) ? id : next.id);
}

/* ---------------------------------------------------- drag and drop */
// A page is never told where a dropped file lives; it is handed the bytes.
// Where the drop does carry a path — some browsers and file managers put one in
// text/uri-list — it is used directly and nothing is copied. Otherwise the file
// is streamed to a scratch copy, which is the only way a browser can open it at
// all, and the copy is deleted when the document is closed.
function pathFromDrop(dt) {
  const uri = (dt.getData("text/uri-list") || "").split("\n")
                .map(s => s.trim()).filter(s => s && !s.startsWith("#"))[0];
  if (uri && uri.startsWith("file://")) {
    try { return decodeURI(new URL(uri).pathname); } catch { return null; }
  }
  const plain = (dt.getData("text/plain") || "").trim();
  if (plain.startsWith("/") || /^[A-Za-z]:[\\/]/.test(plain)) return plain;
  const f = dt.files && dt.files[0];
  if (f && f.path) return f.path;      // present in some embedded browsers
  return null;
}

async function uploadDrop(file) {
  el.busy.classList.add("on");
  el.busyFill.style.width = "0";
  el.busyMsg.textContent = `Copying ${file.name} (${fmtBytes(file.size)})…`;

  try {
    const d = await new Promise((resolve, reject) => {
      const x = new XMLHttpRequest();
      x.open("POST", api("/api/upload", {doc: null}));
      x.setRequestHeader("X-Sqldoc-Filename", encodeURIComponent(file.name).replace(/%20/g, " "));
      x.upload.onprogress = (e) => {
        if (e.lengthComputable) el.busyFill.style.width = (e.loaded / e.total * 100) + "%";
      };
      x.onload = () => {
        let body = {};
        try { body = JSON.parse(x.responseText); } catch {}
        if (x.status >= 200 && x.status < 300 && !body.error) resolve(body);
        else reject(new Error(body.error || `upload failed (${x.status})`));
      };
      x.onerror = () => reject(new Error("upload failed"));
      x.send(file);
    });
    await refreshSession();
    await openDoc(d.id);
  } catch (e) {
    fail(e);
  } finally {
    el.busy.classList.remove("on");
  }
}

function wireOpening() {
  el.docs.onchange = () => openDoc(el.docs.value);
  el.closeBtn.onclick = () => closeDoc(S.docId);
  // Home shows the start page — recent files, the path box, the Open button —
  // rather than jumping straight into a dialog, so there is always a way back
  // to what you opened before. ⌘O is the shortcut for the dialog itself.
  $("#openbtn").onclick = () => { refreshSession().then(showStart); };
  el.startOpen.onclick = pickFile;
  $("#pathform").onsubmit = (e) => {
    e.preventDefault();
    const v = el.pathInput.value.trim();
    if (v) { el.pathInput.value = ""; openPath(v); }
  };

  // dragenter/dragleave fire for every element crossed, so the overlay is
  // driven by a depth counter rather than by the last event seen.
  let depth = 0;
  const hasFile = (e) => [...(e.dataTransfer?.types || [])].includes("Files");

  addEventListener("dragenter", (e) => {
    if (!hasFile(e)) return;
    e.preventDefault();
    if (++depth === 1) el.drop.classList.add("on");
  });
  addEventListener("dragover", (e) => {
    if (!hasFile(e)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "copy";
  });
  addEventListener("dragleave", (e) => {
    if (!hasFile(e)) return;
    if (--depth <= 0) { depth = 0; el.drop.classList.remove("on"); }
  });
  addEventListener("drop", async (e) => {
    if (!hasFile(e)) return;
    e.preventDefault();
    depth = 0;
    el.drop.classList.remove("on");

    const dt = e.dataTransfer;
    const path = pathFromDrop(dt);
    if (path) return openPath(path);

    const file = dt.files && dt.files[0];
    if (!file) return;
    await uploadDrop(file);
  });
}

/* ------------------------------------------------------------- table */
async function setTable(name) {
  const t = S.doc.tables.find(x => x.name === name);
  S.table = name; S.hasRowid = !!(t && t.hasRowid);
  S.blocks.clear(); abortAll(); S.top = 0; S.sort = null;
  resetFind();

  // First window and the count are requested together. The window paints; the
  // count only decides how long the scrollbar is, so it is never waited on.
  const first = getJSON("/api/rows", {table:name, offset:0, limit:BLOCK});
  refreshCount();

  let page;
  try { page = await first; } catch (e) { return fail(e); }

  S.cols = page.columns || [];
  S.blocks.set(0, page);
  showTiming(page);
  measure(page);
  buildHead();
  el.empty.classList.toggle("on", page.rows.length === 0 && !S.count.rows);
  el.err.classList.remove("on");
  invalidate();
  refreshColumnHints();
}

/* --------------------------------------------------------------- gallery */
// A document that is mostly small lookup tables opens into Gallery instead of
// Table view (see defaultView in internal/server) - every non-hidden table
// rendered as its own small grid, one below another, so it can be read top to
// bottom instead of picked through one table at a time from the dropdown.
// Table view stays a click away regardless of which one a document defaults
// into.
const GALLERY_ROW_CAP = 200; // defensive cap if manually toggled onto a bigger table

function firstVisibleTable() {
  const visible = S.doc.tables.filter(t => !t.hidden);
  const list = visible.length ? visible : S.doc.tables;
  return list.length ? list[0].name : null;
}

// showTableView switches to Table view, loading a table only if a different
// one (or none) is already showing - so re-entering Table view after a trip
// through Gallery doesn't re-fetch what's already on screen.
// setTableControlsVisible hides find/zoom/export while Gallery is showing -
// each is meaningless without one active table (find and export both key off
// S.table, which is not necessarily set at all while Gallery has never been
// left). Uses style.display, the same mechanism renderDocBar already uses to
// hide these when no document is open at all, so the two don't fight: this
// only ever runs after renderDocBar, narrowing "a document is open" down to
// "and Table view is what's showing."
function setTableControlsVisible(show) {
  for (const id of ["findbtn","zin","zout","save"]) {
    const n = document.getElementById(id);
    if (n) n.style.display = show ? "" : "none";
  }
}

async function showTableView(name) {
  name = name || S.table || firstVisibleTable();
  S.view = "table";
  el.doc.classList.remove("off");
  el.gallery.classList.remove("on");
  el.viewBtn.classList.remove("on");
  setTableControlsVisible(true);
  if (name && name !== S.table) {
    el.sel.value = name;
    await setTable(name);
  }
}

async function showGalleryView() {
  S.view = "gallery";
  el.doc.classList.add("off");
  el.gallery.classList.add("on");
  el.viewBtn.classList.add("on");
  setTableControlsVisible(false);
  if (!S.galleryLoaded) await openGallery();
}

function toggleView() {
  return S.view === "gallery" ? showTableView() : showGalleryView();
}

// openGallery fetches every non-hidden table's rows (capped, defensively -
// this view is meant for small tables, not a substitute for Table view on a
// large one) and renders each as its own grid.
async function openGallery() {
  S.galleryLoaded = true;
  const docId = S.docId;
  const visible = S.doc.tables.filter(t => !t.hidden);
  const results = await Promise.all(visible.map(async (t) => {
    try { return {t, page: await getJSON("/api/rows", {table:t.name, offset:0, limit:GALLERY_ROW_CAP+1})}; }
    catch (e) { return {t, error: e}; }
  }));
  if (S.docId !== docId) return; // a different document is showing by now
  el.gallery.innerHTML = "";
  for (const r of results) el.gallery.appendChild(renderMiniTable(r));
}

// renderMiniTable builds one table's grid in full - no virtualized scrolling,
// since a gallery table is capped well under the size that would need it (see
// GALLERY_ROW_CAP). Reuses headCell()/writeCell() so a cell renders exactly as
// it would in Table view, and widestText() (shared with measure()/autoFit())
// so sizing is decided the same way everywhere.
function renderMiniTable({t, page, error}) {
  const box = document.createElement("div");
  box.className = "gtable";

  const label = document.createElement("div");
  label.className = "glabel";
  label.innerHTML = `<span class="gname">${esc(t.label)}</span>`;
  label.onclick = () => showTableView(t.name);
  box.appendChild(label);

  if (error) {
    box.appendChild(Object.assign(document.createElement("div"),
      {className: "gempty", textContent: String(error.message || error)}));
    return box;
  }

  const cols = page.columns || [];
  const rows = page.rows || [];
  const shown = Math.min(rows.length, GALLERY_ROW_CAP);
  label.insertAdjacentHTML("beforeend",
    `<span class="gcount">${fmtInt(shown)}${rows.length > GALLERY_ROW_CAP ? "+" : ""} rows</span>` +
    (rows.length > GALLERY_ROW_CAP ? `<span class="gmore">open in Table view →</span>` : ""));

  if (!shown) {
    box.appendChild(Object.assign(document.createElement("div"),
      {className: "gempty", textContent: "Empty."}));
    return box;
  }

  const texts = cols.map((c, i) => rows.slice(0, shown).map(r => cellText(r[i])));
  const widths = cols.map((c, i) => widestText(c.name, c.type, texts[i], {min:56, max:460, truncate:60}));

  const head = document.createElement("div");
  head.className = "ghead";
  cols.forEach((c, i) => head.appendChild(headCell(c, widths[i], {interactive:false})));
  box.appendChild(head);

  const body = document.createDocumentFragment();
  for (let r = 0; r < shown; r++) {
    const row = document.createElement("div");
    row.className = "row";
    cols.forEach((c, i) => {
      const cell = document.createElement("div");
      cell.className = "cell" + (c.numeric ? " num" : "");
      cell.style.width = widths[i] + "px";
      writeCell(cell, rows[r][i], "");
      row.appendChild(cell);
    });
    body.appendChild(row);
  }
  box.appendChild(body);
  return box;
}

// refreshCount polls until the count is exact. The estimate lands immediately,
// the exact value replaces it whenever the background COUNT finishes.
async function refreshCount() {
  const table = S.table;
  for (let i = 0; i < 600; i++) {
    let c;
    try { c = await getJSON("/api/count", {table}); } catch { return; }
    if (S.table !== table) return;
    S.count = c;
    invalidate();
    if (c.exact) return;
    await new Promise(r => setTimeout(r, i < 5 ? 120 : 700));
  }
}

/* ---------------------------------------------- column width measuring */
const ctx2d = document.createElement("canvas").getContext("2d");
const uiFontFamily = `-apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif`;
const gridFont = () => `${13*S.zoom}px ${uiFontFamily}`;
// Chrome text elision needs the exact font a container renders in, not the
// zoomable grid font - #status and the recent list sit outside the grid and
// don't scale with S.zoom.
const statusFont = () => `11px ${uiFontFamily}`;      // matches #status's font-size
const recentPathFont = () => `12px ${uiFontFamily}`;  // matches .recent .rpath's font-size

// elideMiddle drops the middle of a string and keeps roughly equal amounts of
// its start and end, for text with no structure worth exploiting (elidePath,
// below, uses this only as its own fallback). Binary search on the split
// point rather than trimming one character at a time: a status-bar path can
// be a few hundred characters, and a resize fires this on every frame.
function elideMiddle(text, maxWidthPx, font) {
  ctx2d.font = font;
  if (ctx2d.measureText(text).width <= maxWidthPx) return text;
  const ell = "…";
  let lo = 0, hi = Math.floor(text.length / 2);
  while (lo < hi) {
    const mid = Math.ceil((lo + hi) / 2);
    const s = text.slice(0, mid) + ell + text.slice(text.length - mid);
    if (ctx2d.measureText(s).width <= maxWidthPx) lo = mid; else hi = mid - 1;
  }
  return lo === 0 ? ell : text.slice(0, lo) + ell + text.slice(text.length - lo);
}

// elidePath is the Chrome-style version of the above: the filename is always
// shown in full, and the directory prefix grows one path segment at a time
// (never cutting a name mid-word) for as long as "<prefix>/…/<filename>"
// still fits. Falls back to elideMiddle on the filename itself only if the
// filename alone can't fit the container.
function elidePath(path, maxWidthPx, font) {
  ctx2d.font = font;
  if (ctx2d.measureText(path).width <= maxWidthPx) return path;

  const slash = path.lastIndexOf("/");
  const base = slash >= 0 ? path.slice(slash + 1) : path;
  const segs = slash >= 0 ? path.slice(0, slash).split("/") : [];

  if (ctx2d.measureText(base).width > maxWidthPx * 0.9) return elideMiddle(base, maxWidthPx, font);

  let head = "";
  for (let i = 0; i < segs.length; i++) {
    const next = i === 0 ? segs[i] : head + "/" + segs[i];
    if (ctx2d.measureText(next + "/…/" + base).width > maxWidthPx) break;
    head = next;
  }
  return head ? head + "/…/" + base : "…/" + base;
}

// cellText turns a raw cell value (from a fetched page) into the string a
// width measurement scores. Matches writeCell's own NULL/blob handling so a
// column is never sized against something the grid wouldn't actually show.
function cellText(v) {
  return v === null ? "NULL" : typeof v === "object" ? "◼ 000 KB" : String(v);
}

// widestText scores a header against candidate value strings and returns the
// pixel width needed for the widest one, clamped to [min, max]. The one
// measuring routine behind measure(), autoFit(), and applyColumnHints() (the
// background-corrected width from a wider server-side sample), so "how wide
// does this column need to be" is decided one way, not three.
function widestText(name, type, texts, {min, max, truncate}) {
  const font = gridFont();
  ctx2d.font = "600 " + font;
  let w = ctx2d.measureText(name).width + (type ? ctx2d.measureText(" "+type).width*0.85 : 0);
  ctx2d.font = font;
  for (const s of texts) {
    if (s == null) continue;
    const m = ctx2d.measureText(s.length > truncate ? s.slice(0,truncate) : s).width;
    if (m > w) w = m;
  }
  return Math.round(Math.min(max, Math.max(min, w + 22)));
}

function measure(page) {
  // Sampling 40 rows is enough to size a column and costs nothing; measuring
  // every row of the window is what makes naive grids stutter on load. A
  // wider, representative sample from further into the table arrives later,
  // in the background — see refreshColumnHints/applyColumnHints below.
  const step = Math.max(1, Math.floor(page.rows.length / 40));
  S.widths = S.cols.map((c, i) => {
    const texts = [];
    for (let r = 0; r < page.rows.length; r += step) texts.push(cellText(page.rows[r][i]));
    return widestText(c.name, c.type, texts, {min:56, max:460, truncate:60});
  });
  const digits = Math.max(4, String(Math.max(S.count.rows||0, 1000)).length);
  S.gutter = Math.round(digits * 8 * S.zoom + 20);
  S.userSized = false;
  S.slack = null;
  fitToWidth();
}

// refreshColumnHints polls the background column-width sample the server
// builds from a much wider slice of the table than the first page (see
// Doc.ColumnHints in internal/doc/colwidths.go). Fire-and-forget: first paint
// already happened via measure() above and never waits on this.
async function refreshColumnHints() {
  const table = S.table;
  for (let i = 0; i < 20; i++) {
    let h;
    try { h = await getJSON("/api/colwidths", {table}); } catch { return; }
    if (S.table !== table) return;
    if (h.done) { if (h.known) applyColumnHints(h); return; }
    await new Promise(r => setTimeout(r, 300));
  }
}

// applyColumnHints reflows column widths once the background sample lands,
// combining it with whatever rows are already loaded. A resize the person is
// actively dragging (S.userSized) is never overridden by this.
function applyColumnHints(hints) {
  if (S.userSized || S.table !== hints.table) return;
  const texts = S.cols.map(() => []);
  for (const page of S.blocks.values()) {
    for (const r of page.rows) {
      for (let i = 0; i < texts.length; i++) texts[i].push(cellText(r[i]));
    }
  }
  for (let i = 0; i < texts.length && i < hints.samples.length; i++) {
    if (hints.samples[i]) texts[i].push(hints.samples[i]);
  }
  S.widths = S.cols.map((c, i) => widestText(c.name, c.type, texts[i], {min:56, max:460, truncate:60}));
  S.userSized = false;
  S.slack = null;
  fitToWidth();
  buildHead();
  invalidate();
}

// fitToWidth spreads leftover horizontal space across the columns so a narrow
// table fills the window like a page instead of trailing off into an empty
// margin. The space is shared in proportion to each column's natural width:
// handing it all to one column stretches a date or an id into something
// absurd, which looks like a bug even though it fits.
//
// Any slack previously handed out is taken back first, which makes this
// idempotent — it runs again on every resize, and running it twice at one size
// has to leave the widths where they were.
function fitToWidth() {
  if (S.userSized || !S.widths.length) return;

  if (S.slack) {
    for (let i = 0; i < S.slack.length && i < S.widths.length; i++) S.widths[i] -= S.slack[i];
    S.slack = null;
  }

  const natural = S.widths.reduce((a, b) => a + b, 0);
  const avail = el.scroll.clientWidth - S.gutter - natural - 1;

  // Overflowing by a hair still costs a horizontal scrollbar, which reads as
  // clutter rather than as information. A small overshoot is absorbed; a real
  // overflow is left to scroll honestly.
  if (avail < 0 && avail < -Math.max(48, el.scroll.clientWidth * 0.06)) return;
  if (avail === 0 || natural <= 0) return;

  const slack = new Array(S.widths.length).fill(0);
  let handed = 0;
  for (let i = 0; i < S.widths.length; i++) {
    const share = Math.round(avail * (S.widths[i] / natural));
    if (S.widths[i] + share < 48) continue;      // never squeeze to illegible
    slack[i] = share;
    handed += share;
  }
  // Rounding leaves a pixel or two over; give the remainder to the widest
  // column so the total lands exactly on the viewport.
  const rest = avail - handed;
  if (rest !== 0) {
    let widest = 0;
    for (let i = 1; i < S.widths.length; i++) if (S.widths[i] > S.widths[widest]) widest = i;
    if (S.widths[widest] + slack[widest] + rest >= 48) slack[widest] += rest;
  }

  for (let i = 0; i < S.widths.length; i++) S.widths[i] += slack[i];
  S.slack = slack;
}

// headCell builds one column header. Table view's is interactive - click to
// sort, drag the grip to resize; a Gallery mini-table's (opts.interactive
// left false) is neither, since a two-row lookup table has no scroll to sort
// or column worth manually resizing. Shared so the two headers stay visually
// identical rather than drifting into two hand-written versions of the same
// markup.
function headCell(c, width, opts) {
  const d = document.createElement("div");
  d.className = "cell" + (c.numeric ? " num" : "");
  d.style.width = width + "px"; d.style.height = rowH() + "px";
  d.title = `${c.name}${c.type ? " · "+c.type : ""}${c.pk ? " · PRIMARY KEY" : ""}${c.notNull ? " · NOT NULL" : ""}`;
  const arrow = opts.sort && opts.sort.col === c.name ? (opts.sort.desc ? " ▾" : " ▴") : "";
  d.innerHTML = `<span>${esc(c.name)}</span>${c.type ? `<span class="type">${esc(c.type)}</span>` : ""}${arrow ? `<span class="sort">${arrow}</span>` : ""}`;
  if (opts.interactive) {
    d.onclick = () => toggleSort(c.name);
    d.appendChild(grip(opts.index));
  }
  return d;
}

function buildHead() {
  const f = document.createDocumentFragment();
  const g = document.createElement("div");
  g.className = "cell gut"; g.style.width = S.gutter + "px"; g.style.height = rowH() + "px";
  f.appendChild(g);
  S.cols.forEach((c, i) => {
    f.appendChild(headCell(c, S.widths[i], {interactive:true, index:i, sort:S.sort}));
  });
  const fill = document.createElement("div");
  fill.className = "cell fill";
  fill.style.height = rowH() + "px";
  f.appendChild(fill);
  el.head.replaceChildren(f);
}

// grip is the drag handle on a column's right edge. Dragging sets the width;
// double-clicking sizes the column to the widest value currently loaded.
function grip(i) {
  const g = document.createElement("div");
  g.className = "grip";
  g.onclick = (e) => e.stopPropagation();      // resizing is not sorting
  g.ondblclick = (e) => { e.stopPropagation(); autoFit(i); };
  g.onmousedown = (e) => {
    e.preventDefault(); e.stopPropagation();
    const x0 = e.clientX, w0 = S.widths[i];
    document.body.style.cursor = "col-resize";
    S.userSized = true;
    const move = (ev) => {
      S.widths[i] = Math.max(40, Math.round(w0 + ev.clientX - x0));
      sizeColumn(i);
    };
    const up = () => {
      document.body.style.cursor = "";
      removeEventListener("mousemove", move);
      removeEventListener("mouseup", up);
    };
    addEventListener("mousemove", move);
    addEventListener("mouseup", up);
  };
  return g;
}

// sizeColumn writes one column's width straight into the existing cells. Going
// through the full redraw for every mousemove would rebuild rows the drag has
// not touched.
function sizeColumn(i) {
  const w = S.widths[i] + "px";
  el.head.children[i+1].style.width = w;
  for (const row of el.rows.children) {
    const c = row.children[i+1];
    if (c) c.style.width = w;
  }
}

function autoFit(i) {
  const texts = [];
  for (const page of S.blocks.values()) for (const r of page.rows) texts.push(cellText(r[i]));
  S.userSized = true;
  S.widths[i] = widestText(S.cols[i].name, S.cols[i].type, texts, {min:40, max:900, truncate:200});
  sizeColumn(i);
}

function toggleSort(col) {
  if (S.sort && S.sort.col === col) {
    S.sort = S.sort.desc ? null : {col, desc:true};
  } else {
    S.sort = {col, desc:false};
  }
  S.blocks.clear(); abortAll(); S.top = 0;
  buildHead(); invalidate();
}

/* ------------------------------------------------------------ fetching */
function abortAll() {
  for (const c of S.pending.values()) c.abort();
  S.pending.clear();
}

function blockOf(row) { return Math.floor(row / BLOCK); }

function want(b) {
  if (b < 0 || S.blocks.has(b) || S.pending.has(b)) return;
  const total = S.count.known ? S.count.rows : Infinity;
  if (b * BLOCK >= total) return;

  const ac = new AbortController();
  S.pending.set(b, ac);

  const q = {table:S.table, limit:BLOCK, offset:b*BLOCK};
  if (S.sort) { q.sort = S.sort.col; if (S.sort.desc) q.desc = 1; }
  else if (S.hasRowid) {
    // If the block just above is already loaded, anchor on its last rowid and
    // take the index-seek path instead of asking SQLite to count forward.
    const prev = S.blocks.get(b-1);
    if (prev && prev.rowids && prev.rowids.length === BLOCK) q.after = prev.rowids[BLOCK-1];
  }

  getJSON("/api/rows", q, ac.signal).then(page => {
    S.pending.delete(b);
    if (page.table !== S.table) return;
    S.blocks.set(b, page);
    if (S.blocks.size > 60) {                       // bounded cache
      const keep = blockOf(S.top);
      for (const k of [...S.blocks.keys()]) if (Math.abs(k-keep) > 24) S.blocks.delete(k);
    }
    showTiming(page);
    invalidate();
  }).catch(e => {
    S.pending.delete(b);
    if (e.name !== "AbortError") fail(e);
  });
}

function showTiming(page) {
  el.timing.textContent = `${(page.micros/1000).toFixed(1)} ms`;
  el.timing.className = page.micros < 15000 ? "fast" : "slow";
  el.timing.title = `server took the ${page.path} path`;
}

function rowAt(i) {
  const b = S.blocks.get(blockOf(i));
  if (!b) return null;
  const k = i - blockOf(i)*BLOCK;
  return k < b.rows.length ? {cells:b.rows[k], rowid: b.rowids ? b.rowids[k] : 0} : null;
}

/* ------------------------------------------------------------ scrolling */
function totalRows() {
  if (S.count.known) return Math.max(S.count.rows, 0);
  // Until the count lands, extend the document as far as data is known to go
  // so scrolling is never blocked waiting for it.
  let max = 0;
  for (const [b, p] of S.blocks) max = Math.max(max, b*BLOCK + p.rows.length);
  return max + BLOCK;
}

function syncSizer() {
  const total = totalRows();
  const contentH = total * rowH();
  S.scale = contentH > MAX_SIZER ? contentH / MAX_SIZER : 1;
  el.sizer.style.height = Math.max(1, Math.round(contentH / S.scale)) + "px";

  // The sizer also carries the content width. Rows are absolutely positioned
  // against it, so without this they would be capped at the viewport width and
  // a table with many columns could never scroll sideways.
  const contentW = S.gutter + S.widths.reduce((a, b) => a + b, 0);
  el.sizer.style.width = Math.max(contentW, el.scroll.clientWidth) + "px";

  S.viewport = Math.ceil(el.scroll.clientHeight / rowH()) + 1;
}

function clampTop(v) {
  const max = Math.max(0, totalRows() - S.viewport + 1);
  return Math.min(Math.max(0, v), max);
}

function setTop(v, syncBar=true) {
  S.top = clampTop(v);
  if (syncBar) {
    S.syncing = true;
    el.scroll.scrollTop = (S.top * rowH()) / S.scale;
    requestAnimationFrame(() => { S.syncing = false; });
  }
  invalidate();
}

el.scroll.addEventListener("scroll", () => {
  // The header is a sibling of the scroller, not inside it, so that it can stay
  // put vertically. That means it has to be told about horizontal movement.
  el.head.scrollLeft = el.scroll.scrollLeft;
  if (S.syncing) return;
  S.top = clampTop((el.scroll.scrollTop * S.scale) / rowH());
  invalidate();
}, {passive:true});

// A hidden tab stops running animation frames, so a redraw scheduled while
// hidden may be the last one queued. Force one on the way back.
document.addEventListener("visibilitychange", () => {
  if (!document.hidden) { S.dirty = false; invalidate(); }
});

// When the scrollbar is compressed, one pixel covers many rows, so wheel and
// trackpad deltas are applied to the row cursor directly. Below that threshold
// the browser's own scrolling is left alone because it feels better.
el.scroll.addEventListener("wheel", (e) => {
  if (S.scale === 1) return;
  e.preventDefault();
  setTop(S.top + e.deltaY / rowH());
}, {passive:false});

/* ------------------------------------------------------------ rendering */
function invalidate() {
  if (S.dirty) return;
  S.dirty = true;
  requestAnimationFrame(() => { S.dirty = false; draw(); });
}

const pool = [];
function draw() {
  if (!S.docId || !S.cols.length) return;   // the start page owns the window
  syncSizer();
  const first = Math.max(0, Math.floor(S.top) - OVERSCAN);
  const n = S.viewport + OVERSCAN*2;
  const total = totalRows();

  // The rendered window is pinned to the top of the viewport and nudged by the
  // sub-row remainder. Doing it this way keeps the arithmetic identical whether
  // or not the scrollbar is compressed.
  el.rows.style.top = Math.round(el.scroll.scrollTop) + "px";
  el.rows.style.transform = `translateY(${Math.round((first - S.top) * rowH())}px)`;

  const b0 = blockOf(first), b1 = blockOf(first + n);
  for (let b = b0; b <= b1; b++) want(b);
  want(S.lastDir >= 0 ? b1+1 : b0-1);   // prefetch one block ahead

  while (pool.length < n) {
    const d = document.createElement("div");
    d.className = "row";
    el.rows.appendChild(d);
    pool.push(d);
  }
  while (pool.length > n) el.rows.removeChild(pool.pop());

  const cur = S.find.matches[S.find.idx];
  for (let i = 0; i < n; i++) {
    const idx = first + i;
    const node = pool[i];
    if (idx >= total) { node.style.display = "none"; continue; }
    node.style.display = "";
    node.style.height = rowH() + "px";
    const r = rowAt(idx);
    paintRow(node, idx, r, cur);
  }

  el.pos.textContent = renderPos(Math.floor(S.top), total);
  el.meta.innerHTML = renderMeta();
}

function paintRow(node, idx, r, cur) {
  const hit = r && S.find.hits.has(r.rowid);
  node.className = "row" + (r ? "" : " skel") + (hit ? " hit" : "") +
                   (r && cur && cur.rowid === r.rowid ? " cur" : "");

  if (node.childElementCount !== S.cols.length + 2) {
    const f = document.createDocumentFragment();
    const g = document.createElement("div");
    g.className = "cell gut"; f.appendChild(g);
    for (let c = 0; c < S.cols.length; c++) {
      const d = document.createElement("div");
      d.className = "cell" + (S.cols[c].numeric ? " num" : "");
      f.appendChild(d);
    }
    const fill = document.createElement("div");
    fill.className = "cell fill";
    f.appendChild(fill);
    node.replaceChildren(f);
  }

  const kids = node.children;
  kids[0].style.width = S.gutter + "px";
  kids[0].textContent = fmtInt(idx + 1);

  const needle = S.find.q && hit ? S.find.q.toLowerCase() : "";
  for (let c = 0; c < S.cols.length; c++) {
    const cell = kids[c+1];
    cell.style.width = S.widths[c] + "px";
    if (!r) { cell.textContent = ""; continue; }
    writeCell(cell, r.cells[c], needle);
  }
}

function writeCell(cell, v, needle) {
  if (v === null) { cell.innerHTML = '<span class="null">NULL</span>'; return; }
  if (typeof v === "object") { cell.innerHTML = `<span class="blob">◼ ${fmtBytes(v.b)}</span>`; return; }
  let s = String(v);
  if (s.length > 400) s = s.slice(0,400) + "…";
  if (needle) {
    const i = s.toLowerCase().indexOf(needle);
    if (i >= 0) {
      cell.innerHTML = esc(s.slice(0,i)) + "<mark>" + esc(s.slice(i,i+needle.length)) + "</mark>" + esc(s.slice(i+needle.length));
      return;
    }
  }
  cell.textContent = s;   // textContent avoids a parse per cell
}

function renderPos(first, total) {
  const a = Math.min(first+1, total), b = Math.min(first + S.viewport, total);
  if (!total) return "empty";
  return `${fmtInt(a)}–${fmtInt(b)}`;
}

function renderMeta() {
  const c = S.count;
  const rows = !c.known ? "counting…" : `<b>${fmtInt(c.rows)}</b> rows${c.exact ? "" : " (est.)"}`;
  return `${rows} · ${S.cols.length} cols · ${fmtBytes(S.doc.size)}`;
}

const escMap = {"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;"};
function esc(s) { return String(s).replace(/[&<>"]/g, m => escMap[m]); }

/* ---------------------------------------------------------------- find */
function resetFind() {
  Object.assign(S.find, {q:"", matches:[], idx:-1, cursor:0, done:true, running:false, restart:null, hits:new Map()});
  el.findCount.textContent = "";
  el.findProg.style.width = "0";
}

function openFind() {
  el.find.classList.add("open");
  el.findIn.focus(); el.findIn.select();
}
function closeFind() {
  el.find.classList.remove("open");
  resetFind(); invalidate();
  el.scroll.focus();
}

let findTimer = null;
el.findIn.addEventListener("input", () => {
  clearTimeout(findTimer);
  findTimer = setTimeout(() => startFind(el.findIn.value), 180);
});
el.findIn.addEventListener("keydown", (e) => {
  if (e.key === "Enter") { e.preventDefault(); e.shiftKey ? stepFind(-1) : stepFind(1); }
  if (e.key === "Escape") { e.preventDefault(); closeFind(); }
});

async function startFind(q) {
  Object.assign(S.find, {q, matches:[], idx:-1, cursor:0, done:false, hits:new Map()});
  if (!q) { el.findCount.textContent = ""; el.findProg.style.width = "0"; invalidate(); return; }
  if (S.find.running) { S.find.restart = q; return; }
  S.find.running = true;
  const table = S.table;

  // Walk the table in bounded increments, showing hits and progress as they
  // arrive rather than blocking on a whole-table scan. The finally clause is
  // load-bearing: if this loop ever exited without clearing `running`, every
  // later search would be silently dropped.
  try {
    while (!S.find.done && S.find.q === q && S.table === table) {
      let r;
      try { r = await getJSON("/api/find", {table, q, from:S.find.cursor, limit:50}); }
      catch { break; }
      if (S.find.q !== q || S.table !== table) break;

      for (const m of (r.matches || [])) {
        S.find.matches.push(m);
        S.find.hits.set(m.rowid, m.column);
      }
      S.find.cursor = r.next; S.find.done = r.done;
      el.findProg.style.width = Math.round((r.progress||0)*100) + "%";
      el.findCount.textContent = S.find.matches.length
        ? `${S.find.idx+1 || 1}/${fmtInt(S.find.matches.length)}${r.done ? "" : "+"}`
        : (r.done ? "no results" : "searching…");
      if (S.find.idx < 0 && S.find.matches.length) stepFind(1);
      invalidate();
      if (S.find.matches.length >= 5000) break;   // enough to navigate by
    }
  } finally {
    S.find.running = false;
    el.findProg.style.width = "0";
  }

  // A query typed while this one was scanning takes over now.
  const pending = S.find.restart;
  S.find.restart = null;
  if (pending != null && pending !== q) startFind(pending);
}

function stepFind(dir) {
  const m = S.find.matches;
  if (!m.length) return;
  S.find.idx = (S.find.idx + dir + m.length) % m.length;
  el.findCount.textContent = `${S.find.idx+1}/${fmtInt(m.length)}${S.find.done ? "" : "+"}`;
  jumpToRowid(m[S.find.idx].rowid);
}

// jumpToRowid scrolls to the row with the given rowid. The server resolves the
// rowid to a true ordinal so the jump lands on the real position and the normal
// block loading fills the screen; grafting a fetched window in at a guessed
// position would leave the row numbers lying.
async function jumpToRowid(rowid) {
  const table = S.table;
  try {
    const {ordinal} = await getJSON("/api/ordinal", {table, rowid});
    if (S.table !== table) return;
    setTop(Math.max(0, ordinal - Math.floor(S.viewport/3)));
  } catch {}
}

/* --------------------------------------------------------------- input */
function wire() {
  el.sel.onchange = () => showTableView(el.sel.value);
  el.viewBtn.onclick = toggleView;
  $("#findbtn").onclick = () => el.find.classList.contains("open") ? closeFind() : openFind();
  $("#findprev").onclick = () => stepFind(-1);
  $("#findnext").onclick = () => stepFind(1);
  $("#findclose").onclick = closeFind;
  $("#zin").onclick  = () => setZoom(S.zoom * 1.1);
  $("#zout").onclick = () => setZoom(S.zoom / 1.1);
  $("#save").onclick = () => { location.href = api("/api/export", {table:S.table}); };

  new ResizeObserver(() => { fitToWidth(); buildHead(); invalidate(); }).observe(el.scroll);
  new ResizeObserver(updateStatusPath).observe(el.status);
  new ResizeObserver(() => {
    for (const rp of el.recents.querySelectorAll(".rpath")) {
      rp.textContent = elidePath(rp.dataset.full, rp.clientWidth, recentPathFont());
    }
  }).observe(el.recents);

  el.scroll.addEventListener("scroll", () => {
    const t = S.top;
    S.lastDir = t >= (S.prevTop ?? 0) ? 1 : -1;
    S.prevTop = t;
  }, {passive:true});

  addEventListener("keydown", (e) => {
    const meta = e.metaKey || e.ctrlKey;
    if (meta && e.key === "o") { e.preventDefault(); S.session.canPick ? pickFile() : showStart(); return; }
    if (meta && e.key === "w") { e.preventDefault(); closeDoc(S.docId); return; }
    if (!S.docId) return;
    if (S.view !== "table") return; // find/zoom/scroll below are Table view's own
    if (meta && e.key === "f") { e.preventDefault(); openFind(); return; }
    if (meta && (e.key === "=" || e.key === "+")) { e.preventDefault(); setZoom(S.zoom*1.1); return; }
    if (meta && e.key === "-") { e.preventDefault(); setZoom(S.zoom/1.1); return; }
    if (meta && e.key === "0") { e.preventDefault(); setZoom(1); return; }
    if (meta && e.key === "g") { e.preventDefault(); stepFind(e.shiftKey ? -1 : 1); return; }
    if (e.target === el.findIn) return;
    if (e.key === "Escape" && el.find.classList.contains("open")) { closeFind(); return; }

    const page = S.viewport - 2;
    switch (e.key) {
      case "ArrowDown": setTop(S.top+1); break;
      case "ArrowUp":   setTop(S.top-1); break;
      case "PageDown":  setTop(S.top+page); break;
      case "PageUp":    setTop(S.top-page); break;
      case " ":         setTop(S.top + (e.shiftKey ? -page : page)); break;
      case "Home":      setTop(0); break;
      case "End":       setTop(totalRows()); break;
      default: return;
    }
    e.preventDefault();
  });
}

function setZoom(z) {
  S.zoom = Math.min(3, Math.max(0.6, z));
  document.documentElement.style.setProperty("--zoom", S.zoom);
  document.documentElement.style.setProperty("--row-h", rowH()+"px");
  const b0 = S.blocks.get(blockOf(Math.floor(S.top))) || S.blocks.get(0);
  if (b0) measure(b0);
  buildHead();
  invalidate();
}

boot();
})();
