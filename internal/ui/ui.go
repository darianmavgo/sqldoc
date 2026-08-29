// Package ui holds the viewer's front end and the shell that delivers it.
package ui

import (
	"embed"
	"net/http"
	"strings"

	"github.com/darianmavgo/sqldoc/internal/doc"
)

//go:embed app.css app.js
var assets embed.FS

// Handler serves the embedded assets, mostly so they can be fetched separately
// when debugging. The shell inlines them, so a normal load never uses this.
func Handler() http.Handler { return http.FileServer(http.FS(assets)) }

var shellTmpl = `<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{TITLE}}</title>
<style>{{CSS}}</style>
</head>
<body>
<div id="bar">
  <button id="openbtn" title="Open a database (⌘O)">⌂</button>
  <select id="docs" title="Open databases"></select>
  <button id="closebtn" title="Close this database (⌘W)">✕</button>
  <span class="sep"></span>
  <select id="tbl" title="Table"></select>
  <button id="viewbtn" title="Scroll through every small table at once">⊞</button>
  <span class="meta" id="pos">…</span>
  <span class="spacer"></span>
  <span class="meta" id="meta"></span>
  <span class="sep"></span>
  <button id="findbtn" title="Find (⌘F)">⌕</button>
  <button id="zout" title="Zoom out (⌘−)">−</button>
  <button id="zin" title="Zoom in (⌘+)">+</button>
  <button id="save" title="Download this table as CSV">⤓</button>
</div>

<div id="find">
  <input id="findq" placeholder="Find in table" autocomplete="off" spellcheck="false">
  <span class="count" id="findcount"></span>
  <button id="findprev" title="Previous (⇧⏎)">‹</button>
  <button id="findnext" title="Next (⏎)">›</button>
  <button id="findclose" title="Close (Esc)">✕</button>
  <div class="prog" id="findprog"></div>
</div>

<div id="start">
  <div class="startinner">
    <h1>sqldoc</h1>
    <p class="tagline">Drop a SQLite database anywhere on this page, or open one.</p>
    <div class="startactions">
      <button id="startopen">Open a database…</button>
      <form id="pathform">
        <input id="pathinput" placeholder="…or paste a path" autocomplete="off" spellcheck="false">
      </form>
    </div>
    <div id="recents"></div>
    <p class="startfoot" id="startfoot"></p>
  </div>
</div>

<div id="drop"><div class="dropinner"><span class="dropicon">⤓</span><span id="dropmsg">Drop to open</span></div></div>
<div id="busy"><div class="busyinner"><div class="busybar"><i id="busyfill"></i></div><span id="busymsg"></span></div></div>

<div id="doc">
  <div id="sheet">
    <div id="head"></div>
    <div id="scroll" tabindex="0"><div id="sizer"><div id="rows"></div></div></div>
    <div id="empty">This table is empty.</div>
    <div id="err"></div>
  </div>
  <div id="status">
    <span id="timing"></span>
    <span class="path" id="path"></span>
  </div>
</div>

<div id="gallery"></div>

<script>window.__SQLDOC__={token:{{TOKEN}}};</script>
<script>{{JS}}</script>
</body></html>`

// Shell renders the single document the viewer loads. Both the stylesheet and
// the script are inlined: the whole front end is a few tens of kilobytes, and
// inlining means first paint costs exactly one round trip with nothing blocking
// behind it.
func Shell(token string, style doc.Style, name string) []byte {
	css, _ := assets.ReadFile("app.css")
	js, _ := assets.ReadFile("app.js")

	title := style.Title
	if title == "" {
		title = name
	}

	r := strings.NewReplacer(
		"{{CSS}}", string(css),
		"{{JS}}", string(js),
		"{{TOKEN}}", jsString(token),
		"{{TITLE}}", htmlEscape(title),
		"{{NAME}}", htmlEscape(name),
	)
	return []byte(r.Replace(shellTmpl))
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// jsString emits a JS string literal safely, including the closing-tag escape
// that would otherwise let a token end the <script> element.
func jsString(s string) string {
	e := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "<", `\x3c`, ">", `\x3e`)
	return `"` + e.Replace(s) + `"`
}
