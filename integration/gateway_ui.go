package integration

// Package integration adds the two edge Monadic Host Services to the runtime:
// the Edge Ingress Gateway (public HTTP → shared-memory packet frame → router
// cell) and the Vectorized Canvas UI extractor (UI cell → vector draw stream →
// host paint). Guest cells cannot open sockets or touch display hardware, so
// the Go host mediates both boundaries deterministically.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mdcfrancis/flow/appgen"
	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/execution"
	"github.com/mdcfrancis/flow/macro"
	"github.com/mdcfrancis/flow/status"
)

// ProductionGateway bridges public HTTP traffic into the cell topology.
type ProductionGateway struct {
	rm        *execution.RuntimeManager
	routerURN string
	flow      func(kind, source, summary string) // optional data-flow log hook
}

// NewProductionGateway builds a gateway that dispatches to routerURN.
func NewProductionGateway(rm *execution.RuntimeManager, routerURN string) *ProductionGateway {
	return &ProductionGateway{rm: rm, routerURN: routerURN}
}

// ServeHTTP implements the Edge Ingress protocol: read the POST body, map it
// into the inbound packet frame, and register-jump into the router cell.
func (pg *ProductionGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "HDM Ingress requires POST frames", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, execution.InboundEnd-execution.InboundBase))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	status, err := pg.rm.RoutePacket(pg.routerURN, body)
	if err != nil {
		http.Error(w, fmt.Sprintf("HDM Hypervisor Transit Fault: %v", err), http.StatusInternalServerError)
		return
	}
	if pg.flow != nil {
		pg.flow("ingress", pg.routerURN, fmt.Sprintf("%d-byte packet → kernel signal %d", len(body), status))
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Transaction processed. Kernel Signal: %d\n", status)
}

// InputServer is the edge half of the Monadic Peripheral Input Gateway: it
// accepts normalized keyboard/mouse events over HTTP and writes them into the
// HMI Input Event Register in shared memory, where guest cells poll them. Guest
// cells cannot read native windowing interrupts, so the host mediates.
type InputServer struct {
	rm   *execution.RuntimeManager
	flow func(kind, source, summary string) // optional data-flow log hook
}

// NewInputServer builds an input gateway that writes into rm's HMI register.
func NewInputServer(rm *execution.RuntimeManager) *InputServer {
	return &InputServer{rm: rm}
}

// inputWire is the JSON envelope the browser posts per peripheral event. X/Y
// are canvas-space integers (the page maps display pixels down to 320x240).
type inputWire struct {
	Type      string `json:"type"` // move|down|up|click|keydown|keyup
	X         int32  `json:"x"`
	Y         int32  `json:"y"`
	Buttons   uint32 `json:"buttons"`
	Modifiers uint32 `json:"mods"`
	Key       uint32 `json:"key"`
}

var inputTypeCodes = map[string]uint32{
	"move":    execution.EvMove,
	"down":    execution.EvMouseDn,
	"up":      execution.EvMouseUp,
	"click":   execution.EvClick,
	"keydown": execution.EvKeyDown,
	"keyup":   execution.EvKeyUp,
}

// ServeHTTP decodes one peripheral event and latches it into the HMI register.
func (is *InputServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "input requires POST", http.StatusMethodNotAllowed)
		return
	}
	var ev inputWire
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&ev); err != nil {
		http.Error(w, "bad event: "+err.Error(), http.StatusBadRequest)
		return
	}
	code, ok := inputTypeCodes[ev.Type]
	if !ok {
		http.Error(w, "unknown event type: "+ev.Type, http.StatusBadRequest)
		return
	}
	if err := is.rm.WriteInputEvent(execution.InputEvent{
		Type: code, X: ev.X, Y: ev.Y, Buttons: ev.Buttons, Modifiers: ev.Modifiers, Key: ev.Key,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Log discrete events (click/down/up/key); mouse moves are high-frequency and
	// low-information, so they refresh the register without flooding the flow log.
	if is.flow != nil && ev.Type != "move" {
		is.flow("input", "hmi", fmt.Sprintf("%s (%d,%d) buttons=%d key=%d", ev.Type, ev.X, ev.Y, ev.Buttons, ev.Key))
	}
	w.WriteHeader(http.StatusNoContent)
}

// CanvasUiEngine extracts vector draw streams from UI cells.
type CanvasUiEngine struct {
	rm *execution.RuntimeManager
}

// NewCanvasUiEngine builds a canvas extractor.
func NewCanvasUiEngine(rm *execution.RuntimeManager) *CanvasUiEngine {
	return &CanvasUiEngine{rm: rm}
}

// ExtractActiveFrame ticks the UI cell and returns its emitted vector stream.
func (cue *CanvasUiEngine) ExtractActiveFrame(cellURN string) ([]byte, error) {
	return cue.rm.RenderFrame(cellURN)
}

// CanvasServer serves a live view of a UI cell's vector framebuffer: an HTML
// page with a <canvas> that polls the raw vector draw stream and paints it. The
// active cell can be switched at runtime (e.g. to a freshly grown app's UI).
type CanvasServer struct {
	engine *CanvasUiEngine
	mu     sync.Mutex
	active string
}

// NewCanvasServer builds a canvas viewer defaulting to cellURN.
func NewCanvasServer(engine *CanvasUiEngine, cellURN string) *CanvasServer {
	return &CanvasServer{engine: engine, active: cellURN}
}

// SetActive focuses the viewer on a different UI cell.
func (cs *CanvasServer) SetActive(urn string) {
	cs.mu.Lock()
	cs.active = urn
	cs.mu.Unlock()
}

// Active returns the currently focused UI cell URN.
func (cs *CanvasServer) Active() string {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.active
}

// Frame returns the raw vector draw stream (24-byte records) for the active UI
// cell, or for the cell named in the ?cell= query parameter.
func (cs *CanvasServer) Frame(w http.ResponseWriter, r *http.Request) {
	cell := cs.Active()
	if q := strings.TrimSpace(r.URL.Query().Get("cell")); q != "" {
		cell = q
	}
	frame, err := cs.engine.ExtractActiveFrame(cell)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Binary canvas frame served as octet-stream — not HTML, so not an XSS sink.
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(frame)
}

// BuildServer runs the Growth Pipeline on an operator-supplied natural-language
// objective. It scaffolds every subsystem in the background (enrolling each as
// it comes online) and returns immediately; the background evolutionary loop
// then builds them ALL up alongside each other (parallel co-evolution).
type BuildServer struct {
	grower   *appgen.Grower
	registry *evolution.CellRegistry
	canvas   *CanvasServer   // optional; auto-focused on a grown app's UI cell
	baseCtx  context.Context // lifecycle ctx for background scaffolding (set by Serve)
}

// NewBuildServer wires a build endpoint to the grower and the cell registry. If
// canvas is non-nil, a build auto-focuses it on the app's UI cell.
func NewBuildServer(grower *appgen.Grower, registry *evolution.CellRegistry, canvas *CanvasServer) *BuildServer {
	return &BuildServer{grower: grower, registry: registry, canvas: canvas}
}

// ServeHTTP accepts a POST whose body is the natural-language objective, grows
// the application, and returns the envelope namespace and scaffolded subsystems.
func (bs *BuildServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "build requires POST", http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	objective := strings.TrimSpace(string(body))
	w.Header().Set("Content-Type", "application/json")
	if objective == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "empty objective"})
		return
	}

	// Chat-like refinement: if the console is focused on an existing app, a new
	// prompt REFINES that app (updates its objective; the loop adapts) rather than
	// spawning a duplicate. Only with no active app does Build grow a fresh one.
	// Otherwise compile the plan now (fast), respond immediately, and scaffold the
	// subsystems in the background — the loop co-evolves them all alongside.
	var (
		env     *appgen.AppEnvelope
		err     error
		refined bool
	)
	if bs.canvas != nil {
		if target := evolution.AppNamespaceOf(bs.canvas.Active()); target != "" && bs.grower.AppExists(target) {
			env, err = bs.grower.Refine(r.Context(), target, objective)
			refined = true
		}
	}
	if env == nil && err == nil {
		env, err = bs.grower.CompileEnvelope(r.Context(), objective)
	}
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}
	ids := make([]string, 0, len(env.SubsystemRequirements))
	uiURN := ""
	for _, s := range env.SubsystemRequirements {
		ids = append(ids, s.Identity)
		if uiURN == "" && s.IsRender() {
			uiURN = s.Identity
		}
	}
	enroll := func(urn string) {
		bs.registry.Add(urn)
		if bs.canvas != nil && urn == uiURN {
			bs.canvas.SetActive(urn) // focus once the UI cell actually exists
		}
	}
	base := bs.baseCtx
	if base == nil {
		base = context.Background()
	}
	go func() { _, _, _ = bs.grower.ScaffoldAll(base, env, enroll) }()

	_ = json.NewEncoder(w).Encode(map[string]any{
		"namespace": env.ApplicationNamespace, "subsystems": ids, "ui": uiURN,
		"parallel": true, "refined": refined,
	})
}

// Page serves the canvas viewer HTML.
func (cs *CanvasServer) Page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, canvasPage)
}

const canvasPage = `<!doctype html><html><head><meta charset="utf-8">
<title>HDM Console</title>
<style>
body{background:#11131a;color:#c8d0e0;font:14px system-ui;margin:0;padding:24px;max-width:1400px}
h1{font-size:15px;font-weight:600;letter-spacing:.02em}
canvas{background:#000;border:1px solid #2a2f3a;image-rendering:pixelated}
input,select,button,textarea{font:13px system-ui;background:#1b1f2a;color:#c8d0e0;border:1px solid #2a2f3a;border-radius:6px;padding:8px}
input{width:60%}
#obj{flex:1 1 auto;min-height:64px;font-size:15px;padding:12px;resize:vertical;font-family:inherit;line-height:1.4}
button{background:#2b6cb0;border-color:#2b6cb0;cursor:pointer}
.row{display:flex;gap:8px;align-items:center;margin:12px 0}
.muted{color:#6b7280}
ul{list-style:none;padding:0;columns:2}
li{padding:2px 0;font-family:ui-monospace,monospace;font-size:12px}
#statuspanel{border:1px solid #2a2f3a;border-radius:8px;padding:12px;margin:12px 0;background:#161922}
.phase{display:flex;align-items:center;gap:10px;margin-bottom:10px}
#phasechip{font-weight:600;text-transform:uppercase;letter-spacing:.04em;font-size:11px;padding:4px 10px;border-radius:20px;color:#fff;transition:background .4s}
#reason{font-size:13px}
.cellgrid{display:flex;flex-wrap:wrap;gap:6px;margin-bottom:10px}
.chip{font-family:ui-monospace,monospace;font-size:11px;padding:3px 8px;border-radius:6px;color:#e6edf7;border:1px solid #0004;transition:background .4s}
.chip.pulse{animation:pulse 1s ease-out}
@keyframes pulse{from{box-shadow:0 0 0 3px rgba(255,213,79,.85)}to{box-shadow:0 0 0 0 transparent}}
.feed{list-style:none;padding:0;columns:1;font-family:ui-monospace,monospace;font-size:11px;max-height:150px;overflow:auto}
.feed li{padding:1px 0}
.feed b{color:#ffd54f;text-transform:uppercase;font-size:10px}
/* Full-viewport-width data-flow log: break out of the centered body column so
   the trace has the whole window, as a single column of rows. */
#flowpanel{border:1px solid #2a2f3a;border-top:none;border-bottom:none;padding:12px 24px;margin:12px 0;background:#0f1420;
  width:100vw;position:relative;left:50%;margin-left:-50vw;box-sizing:border-box}
#flowpanel h2{font-size:12px;font-weight:600;letter-spacing:.04em;color:#8fa0bf;margin:0 0 8px}
.flow{list-style:none;padding:0;margin:0;font-family:ui-monospace,monospace;font-size:12px;max-height:60vh;overflow:auto}
.flow li{padding:3px 6px;display:flex;gap:12px;align-items:baseline;border-bottom:1px solid #161c28;border-radius:4px;cursor:default}
.flow li:hover{background:#182034}
/* full detail is hidden until hover; the compact summary shows by default */
.flow li .more{display:none;color:#8fa0bf}
.flow li:hover .more{display:inline}
.flow .k{flex:0 0 62px;text-transform:uppercase;font-size:9px;font-weight:700;letter-spacing:.05em}
.flow .src{flex:0 0 300px;color:#6b7280;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.flow li:hover .src{overflow:visible;text-overflow:clip}
.flow .sum{flex:1 1 auto;color:#c8d0e0;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.flow li:hover .sum{white-space:normal;overflow:visible}
.flow .t{flex:0 0 auto;color:#4b5563;font-size:10px}
.k-infer{color:#a78bfa}.k-tick{color:#38bdf8}.k-input{color:#fbbf24}.k-ingress{color:#34d399}.k-render{color:#f472b6}
#inspector{border:1px solid #2a2f3a;border-radius:8px;padding:12px;margin:12px 0;background:#161922}
#inspector h2{font-size:12px;font-weight:600;letter-spacing:.04em;color:#8fa0bf;margin:0 0 8px}
#inspector .empty{color:#6b7280;font-size:12px}
#inspector .grp{margin:10px 0 4px;color:#8fa0bf;font-size:11px;text-transform:uppercase;letter-spacing:.04em}
#inspector table{border-collapse:collapse;width:100%;font-family:ui-monospace,monospace;font-size:11px}
#inspector td{padding:2px 10px 2px 0;vertical-align:top;border-bottom:1px solid #161c28}
#inspector pre.src{font-family:ui-monospace,monospace;font-size:11px;line-height:1.4;color:#b8e6c0;background:#0d1117;border:1px solid #1c2532;border-radius:6px;padding:8px;margin:4px 0;white-space:pre;overflow:auto;max-height:40vh}
#inspector .pass{color:#34d399}#inspector .fail{color:#f87171}
#inspector .off{color:#a78bfa}
#cells li{cursor:pointer}#cells li:hover{color:#fff;text-decoration:underline}
#mappanel,#planpanel,#walkpanel,#statelivepanel,#logpanel{border:1px solid #2a2f3a;border-radius:8px;padding:12px;margin:12px 0;background:#0f1420}
#mappanel h2,#planpanel h2,#walkpanel h2,#statelivepanel h2,#logpanel h2{font-size:12px;font-weight:600;letter-spacing:.04em;color:#8fa0bf;margin:0 0 8px}
#appmap,#appplan,#appwalk,#appstate,#applog{font-family:ui-monospace,monospace;font-size:11px;line-height:1.45;color:#c8d0e0;white-space:pre-wrap;margin:0;max-height:50vh;overflow:auto}
#appmap.empty,#appplan.empty,#appwalk.empty,#appstate.empty,#applog.empty{color:#6b7280}
</style></head>
<body>
<h1>HDM — Homeostatic Dataflow Machine · console</h1>

<div class="row">
  <textarea id="obj" placeholder="Describe the application… e.g. build a game of life&#10;&#10;This box is a running prompt — each Build submits the whole text and builds on it. Edit or trim it and Build again."></textarea>
  <button onclick="build()">Build</button>
</div>
<p class="muted" style="font-size:12px;margin:2px 0">The whole box is submitted each time and kept afterwards (saved across reloads). ⌘/Ctrl+Enter to Build.</p>
<p id="buildmsg" class="muted"></p>

<div id="statuspanel">
  <div class="phase"><span id="phasechip">booting</span><span id="reason" class="muted"></span></div>
  <div id="cellgrid" class="cellgrid"></div>
  <ul id="feed" class="feed"></ul>
</div>

<div id="flowpanel">
  <h2>DATA FLOW — live</h2>
  <ul id="flow" class="flow"></ul>
</div>

<div class="row">
  <label class="muted">canvas cell</label>
  <select id="cellsel"><option value="">(active)</option></select>
</div>
<canvas id="c" width="320" height="240" tabindex="0" style="width:640px;height:480px;cursor:crosshair;outline:none"></canvas>
<p id="s" class="muted"></p>
<p class="muted">Click / move / type over the canvas — events route through the HMI input register.</p>

<h1>Cells</h1>
<p class="muted" style="font-size:12px;margin:2px 0">Click a cell to inspect its tests &amp; constraints.</p>
<ul id="cells"></ul>

<div id="inspector">
  <h2>CELL INSPECTOR</h2>
  <div id="inspectbody"><span class="empty">Select a cell above.</span></div>
</div>

<div id="planpanel">
  <h2>SYSTEM PLAN — overview &amp; choreography (each cell's own plan is in its inspector)</h2>
  <pre id="appplan" class="empty">no plan yet</pre>
</div>

<div id="walkpanel">
  <h2>GOAL TREE — the depth-first walk (a fractured goal's children finish before the next sibling)</h2>
  <pre id="appwalk" class="empty">no goals yet</pre>
</div>

<div id="mappanel">
  <h2>APPLICATION MAP — the system as it actually stands (fed into every synthesis)</h2>
  <pre id="appmap" class="empty">no application grown yet</pre>
</div>

<div id="statelivepanel">
  <h2>LIVE STATE — the shared-state contract fields, read from memory now</h2>
  <pre id="appstate" class="empty">no state yet</pre>
</div>

<div id="logpanel">
  <h2>SYSTEM LOG — recent</h2>
  <pre id="applog" class="empty">no log yet</pre>
</div>

<script>
const cv=document.getElementById('c'),ctx=cv.getContext('2d'),sel=document.getElementById('cellsel');
function col(v){return 'rgba('+((v>>>24)&255)+','+((v>>>16)&255)+','+((v>>>8)&255)+','+((v&255)/255).toFixed(3)+')';}
async function draw(){
  try{
    const q=sel.value?('?cell='+encodeURIComponent(sel.value)):'';
    const buf=await (await fetch('/canvas/frame'+q,{cache:'no-store'})).arrayBuffer();
    const dv=new DataView(buf); ctx.clearRect(0,0,cv.width,cv.height);
    // Stratified compositor: bucket records by layer (op high byte) and paint
    // 0→1→2 so the overlay/cursor plane lands on top regardless of emit order.
    const layers=[[],[],[]]; let n=0;
    for(let o=0;o+24<=buf.byteLength;o+=24){
      const op=dv.getInt32(o,true); if(op===0)break; n++;
      const L=(op>>>8)&0xFF, prim=op&0xFF;
      (layers[L]||layers[0]).push({prim,
        a:dv.getInt32(o+4,true),b:dv.getInt32(o+8,true),
        c:dv.getInt32(o+12,true),d:dv.getInt32(o+16,true),rgba:col(dv.getUint32(o+20,true))});
    }
    for(let L=0;L<3;L++)for(const r of layers[L]){
      if(r.prim===1){ctx.fillStyle=r.rgba;ctx.fillRect(r.a,r.b,r.c,r.d);}
      else if(r.prim===2){ctx.strokeStyle=r.rgba;ctx.lineWidth=2;ctx.beginPath();ctx.moveTo(r.a,r.b);ctx.lineTo(r.c,r.d);ctx.stroke();}
      else if(r.prim===3){ctx.fillStyle=r.rgba;ctx.beginPath();ctx.arc(r.a,r.b,r.c,0,6.283);ctx.fill();}
    }
    document.getElementById('s').textContent=buf.byteLength+' bytes · '+n+' primitives';
  }catch(e){document.getElementById('s').textContent='no frame: '+e;}
}
// Peripheral Input Gateway: map pointer/key events into canvas space and post
// them to /input, where the host latches them into the HMI event register.
function mods(e){return (e.shiftKey?1:0)|(e.ctrlKey?2:0)|(e.altKey?4:0)|(e.metaKey?8:0);}
function cxy(e){const r=cv.getBoundingClientRect();return {x:Math.round((e.clientX-r.left)*cv.width/r.width),y:Math.round((e.clientY-r.top)*cv.height/r.height)};}
function post(ev){fetch('/input',{method:'POST',body:JSON.stringify(ev),headers:{'Content-Type':'application/json'}}).catch(()=>{});}
let lastMove=0;
cv.addEventListener('mousemove',e=>{const t=performance.now();if(t-lastMove<40)return;lastMove=t;const p=cxy(e);post({type:'move',x:p.x,y:p.y,buttons:e.buttons,mods:mods(e)});});
cv.addEventListener('mousedown',e=>{const p=cxy(e);post({type:'down',x:p.x,y:p.y,buttons:e.buttons,mods:mods(e)});});
cv.addEventListener('mouseup',e=>{const p=cxy(e);post({type:'up',x:p.x,y:p.y,buttons:e.buttons,mods:mods(e)});});
cv.addEventListener('click',e=>{cv.focus();const p=cxy(e);post({type:'click',x:p.x,y:p.y,buttons:1,mods:mods(e)});});
cv.addEventListener('keydown',e=>{post({type:'keydown',x:0,y:0,key:e.keyCode,mods:mods(e)});if(e.key===' ')e.preventDefault();});
cv.addEventListener('keyup',e=>{post({type:'keyup',x:0,y:0,key:e.keyCode,mods:mods(e)});});
async function refreshCells(){
  try{
    const cells=await (await fetch('/cells',{cache:'no-store'})).json();
    document.getElementById('cells').innerHTML=cells.map(u=>'<li onclick="inspect(\''+u+'\')" title="'+u+'">'+shortURN(u)+'</li>').join('');
    const cur=sel.value;
    sel.innerHTML='<option value="">(active)</option>'+cells.map(u=>'<option>'+u+'</option>').join('');
    sel.value=cur;
  }catch(e){}
}
// Cell inspector: pull a cell's acceptance tests + shared-state constraints.
let inspectURN='';
async function inspect(u){
  inspectURN=u;
  const b=document.getElementById('inspectbody');
  b.innerHTML='<span class="empty">loading '+shortURN(u)+'…</span>';
  try{
    const d=await (await fetch('/cell?urn='+encodeURIComponent(u),{cache:'no-store'})).json();
    let h='<div><b>'+shortURN(u)+'</b> ';
    if(d.total>0){const ok=d.passed>=d.total;h+='<span class="'+(ok?'pass':'fail')+'">'+d.passed+'/'+d.total+' checks</span>';}
    else h+='<span class="empty">no acceptance checks</span>';
    if(d.state)h+=' <span class="muted">· '+d.state+'</span>';
    h+='</div>';
    // THIS cell's design plan (what it must implement) leads; the checks verify it.
    const P=d.plan;
    if(P){
      h+='<div class="grp">Design plan</div>';
      if(P.purpose)h+='<div class="muted" style="margin:2px 0">'+esc(P.purpose)+'</div>';
      const io=[];
      if((P.reads||[]).length)io.push('reads '+esc(P.reads.join(', ')));
      if((P.writes||[]).length)io.push('writes '+esc(P.writes.join(', ')));
      if(io.length)h+='<div style="font-size:11px;color:#8fa0bf">'+io.join(' · ')+'</div>';
      if((P.steps||[]).length){h+='<ol style="margin:4px 0 4px 18px;padding:0;font-size:11px">';
        P.steps.forEach(s=>{h+='<li>'+esc(s)+'</li>';});h+='</ol>';}
      if((P.interactions||[]).length){h+='<div class="muted" style="font-size:11px">connects: '+esc(P.interactions.join('; '))+'</div>';}
      if((P.invariants||[]).length){h+='<div class="muted" style="font-size:11px">invariants: '+esc(P.invariants.join('; '))+'</div>';}
    }
    const T=d.tests||[],S=d.scenarios||[],C=(d.contract&&d.contract.fields)||[];
    if(T.length){h+='<div class="grp">Scalar tests</div><table>';
      T.forEach(t=>{h+='<tr><td>'+esc(t.name||'')+'</td><td>in '+t.input+'</td><td>→ '+t.expected+'</td></tr>';});h+='</table>';}
    if(S.length){h+='<div class="grp">Scenarios</div><table>';
      S.forEach(s=>{h+='<tr><td>'+esc(s.name||'')+'</td><td>'+esc(s.entry||'run-tick')+'</td><td>'+esc(s.expect||'')+'</td></tr>';});h+='</table>';}
    if(C.length){h+='<div class="grp">Shared-state contract</div><table>';
      C.forEach(f=>{h+='<tr><td class="off">'+f.offset+'</td><td>'+esc(f.name||'')+' <span class="muted">'+esc(f.type||'')+'</span></td><td class="muted">'+esc(f.desc||'')+'</td></tr>';});h+='</table>';}
    if(!T.length&&!S.length&&!C.length&&!P)h+='<span class="empty">no plan, tests, or constraints recorded</span>';
    b.innerHTML=h;
    // Source genome: the Flux (cell …) program the cell was authored in (or its
    // WAT). Fetched separately and appended so a slow/absent /flux never blocks the
    // rest of the inspector.
    try{
      const src=await (await fetch('/flux?urn='+encodeURIComponent(u),{cache:'no-store'})).text();
      if(inspectURN===u&&src&&src.trim()){
        const lang=src.trim().startsWith('(cell')?'Flux':'WAT';
        const code=document.createElement('div');
        code.innerHTML='<div class="grp">Source ('+lang+')</div><pre class="src">'+esc(src.trim())+'</pre>';
        b.appendChild(code);
      }
    }catch(e){}
  }catch(e){b.innerHTML='<span class="empty">inspect failed: '+esc(''+e)+'</span>';}
}
// The build box is a persistent, editable prompt — a chat-like composer. Each
// Build submits the WHOLE current text (so a prompt builds on the prior); the
// text is never auto-cleared and survives reloads (localStorage), so it can be
// extended or trimmed and resubmitted as a whole.
const objEl=document.getElementById('obj');
objEl.value=localStorage.getItem('hdm_obj')||'';
// Seed the box with the active app's objective (what the system is building
// toward) when it is empty — so the prompt we are working toward is always
// present. A non-empty box (the operator's running prompt) is left untouched.
if(!objEl.value.trim()){
  fetch('/objective',{cache:'no-store'}).then(r=>r.json()).then(j=>{
    if(j.objective&&!objEl.value.trim()){objEl.value=j.objective;localStorage.setItem('hdm_obj',j.objective);}
  }).catch(()=>{});
}
objEl.addEventListener('input',()=>localStorage.setItem('hdm_obj',objEl.value));
// Cmd/Ctrl+Enter submits, like a chat composer; plain Enter inserts a newline.
objEl.addEventListener('keydown',e=>{if(e.key==='Enter'&&(e.metaKey||e.ctrlKey)){e.preventDefault();build();}});
async function build(){
  const o=objEl.value.trim(); if(!o)return;
  localStorage.setItem('hdm_obj',objEl.value); // keep the whole prompt available
  const m=document.getElementById('buildmsg'); m.textContent='growing… (this calls the model per subsystem; can take a while)';
  try{
    const r=await fetch('/build',{method:'POST',body:o});
    const j=await r.json();
    if(j.error){m.textContent='error: '+j.error;return;}
    const n=(j.subsystems||[]).length;
    m.textContent=(j.refined
      ? 'refined '+j.namespace+' ('+o.length+' chars) — the loop will adapt the existing '+n+' subsystems to the updated objective'
      : 'submitted '+o.length+' chars → '+j.namespace+' · scaffolding '+n+' subsystems (co-evolving in parallel — watch the data-flow log)')
      +(j.ui?(' · viewing '+j.ui):'');
    // The prompt text stays in the box (not cleared) so the next Build can build
    // on it or edit it. Follow the server's freshly-focused active cell.
    sel.value='';
    await refreshCells(); draw();
  }catch(e){m.textContent='build failed: '+e;}
}
sel.onchange=draw;
// Live activity: phase (what/why), cell-state chips, and an event feed.
const PHASE={idle:'#3b4252',booting:'#3b4252',ticking:'#2b6cb0',evolving:'#7c3aed',
  synthesizing:'#7c3aed',grading:'#0891b2',committing:'#059669',fusing:'#d97706',
  splitting:'#d97706',growing:'#059669',waiting:'#b91c1c'};
const CST={live:'#243024',mutating:'#5b4a1f',new:'#1e3a5f',split:'#4a2f5f',fused:'#5f3a1f',rejected:'#5f2020'};
function shortURN(u){return (u||'').replace(/^urn:hdm:/,'');}
let prevCell={};
async function refreshStatus(){
  try{
    const s=await (await fetch('/status',{cache:'no-store'})).json();
    const pc=document.getElementById('phasechip');
    pc.textContent=s.phase||'idle'; pc.style.background=PHASE[s.phase]||'#3b4252';
    document.getElementById('reason').textContent=s.reason||'';
    const grid=(s.cells||[]).map(c=>{
      const changed=prevCell[c.urn]!==undefined&&prevCell[c.urn]!==c.state;
      return '<span class="chip'+(changed?' pulse':'')+'" style="background:'+(CST[c.state]||'#243024')+
        '" title="'+c.urn+'">'+shortURN(c.urn)+' · '+c.state+'</span>';
    }).join('');
    document.getElementById('cellgrid').innerHTML=grid;
    prevCell={}; (s.cells||[]).forEach(c=>prevCell[c.urn]=c.state);
    document.getElementById('feed').innerHTML=(s.events||[]).slice().reverse().slice(0,10)
      .map(e=>'<li><b>'+e.kind+'</b> '+shortURN(e.cell)+' <span class="muted">'+(e.detail||'')+'</span></li>').join('');
    // Live data-flow log: what actually moved through the system, newest first.
    document.getElementById('flow').innerHTML=(s.flow||[]).slice().reverse().slice(0,200)
      .map(f=>{
        const full=esc((f.kind||'')+' · '+(f.source||'')+' · '+(f.summary||'')+' · '+hhmmss(f.ts));
        return '<li title="'+full+'"><span class="k k-'+f.kind+'">'+f.kind+'</span>'+
          '<span class="src">'+esc(shortURN(f.source||''))+'</span>'+
          '<span class="sum">'+esc(f.summary||'')+
            '<span class="more">  ['+esc(f.source||'')+']</span></span>'+
          '<span class="t">'+hhmmss(f.ts)+'</span></li>';
      }).join('');
  }catch(e){}
}
function esc(s){return (s||'').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');}
function hhmmss(ms){if(!ms)return '';const d=new Date(ms);return d.toTimeString().slice(0,8);}
setInterval(draw,1000); setInterval(refreshCells,3000); setInterval(refreshStatus,700);
setInterval(()=>{if(inspectURN)inspect(inspectURN);},4000);
async function refreshMap(){
  try{
    const t=await (await fetch('/map',{cache:'no-store'})).text();
    const el=document.getElementById('appmap');
    if(t&&t.trim()){el.textContent=t;el.classList.remove('empty');}
  }catch(e){}
}
setInterval(refreshMap,5000); refreshMap();
async function refreshPlan(){
  try{
    const t=await (await fetch('/plan',{cache:'no-store'})).text();
    const el=document.getElementById('appplan');
    if(t&&t.trim()){el.textContent=t;el.classList.remove('empty');}
  }catch(e){}
}
setInterval(refreshPlan,5000); refreshPlan();
async function refreshWalk(){
  try{
    const t=await (await fetch('/walk',{cache:'no-store'})).text();
    const el=document.getElementById('appwalk');
    if(t&&t.trim()){el.textContent=t;el.classList.remove('empty');}
  }catch(e){}
}
setInterval(refreshWalk,5000); refreshWalk();
async function refreshState(){
  try{
    const rows=await (await fetch('/state',{cache:'no-store'})).json();
    const el=document.getElementById('appstate');
    if(Array.isArray(rows)&&rows.length){
      el.textContent=rows.map(r=>r.name.padEnd(16)+' '+r.offset.padEnd(10)+' = '+r.value).join('\n');
      el.classList.remove('empty');
    }
  }catch(e){}
}
setInterval(refreshState,700); refreshState();
async function refreshLog(){
  try{
    const t=await (await fetch('/log?n=200',{cache:'no-store'})).text();
    const el=document.getElementById('applog');
    if(t&&t.trim()){el.textContent=t;el.classList.remove('empty');el.scrollTop=el.scrollHeight;}
  }catch(e){}
}
setInterval(refreshLog,2000); refreshLog();
draw(); refreshCells(); refreshStatus();
</script></body></html>`

// Services bundles the optional edge HTTP services to register.
type Services struct {
	Gateway *ProductionGateway
	Canvas  *CanvasServer
	Build   *BuildServer
	Input   *InputServer
	Cells   func() []string
	Status  func() status.Snapshot
	Flow    func(kind, source, summary string) // optional data-flow log sink
	Inspect func(urn string) any               // optional per-cell tests+constraints
	// Objective returns the natural-language objective of the currently active
	// application (what the system is building toward), for seeding the console's
	// build box. Optional.
	Objective func() string
	// AppMap renders the active application's evolving conceptual map (components,
	// what each verifiably does, shared-state data flow and its gaps). Optional.
	AppMap func() string
	// Plan renders the active application's evolving DESIGN PLAN (the system
	// overview, choreography, and each component's algorithm). Optional.
	Plan func() string
	// Walk renders the active application's GOAL TREE — the recursive, depth-first
	// walk the scheduler follows (a fractured goal's children before its siblings).
	// Optional.
	Walk func() string
	// Log returns the tail of the system log (oldest first) for read-only
	// inspection. Optional.
	Log func() []string
	// State returns the live values of the active application's shared-state
	// contract fields (read out of shared memory). Optional.
	State func() any
	// Perf returns per-cell frame-budget telemetry (avg per-frame cost vs the
	// budget, and whether each cell is over). Optional.
	Perf func() any
	// Vision asks the multimodal model a natural-language question about the active
	// app's recently rendered frames — the vision path. Optional.
	Vision func(question string) (string, error)
	// Mask toggles read/write mask enforcement at runtime (for A/B diagnostics) and
	// returns the resulting state string. Optional.
	Mask func(on bool) string
	// Feedback ingests free-form operator commentary about the current solution and
	// routes it into system/app guidance; returns the routed entries. Optional.
	Feedback func(commentary string) any
	// Guidance returns the current system + app guidance for inspection. Optional.
	Guidance func() any
	// OptimizePrompt refines a named system prompt from feedback (LLM rewrite +
	// adversarial validation); returns the outcome. Optional.
	OptimizePrompt func(name, feedback string) any
	// Prompts lists the refinable prompt names and which currently have an override. Optional.
	Prompts func() any
	// Epoch checkpoints/restores/lists the evolvable system baseline (sys cells + prompts +
	// guidance). action is "save" | "restore" | "list". Optional.
	Epoch func(action, name string) any
	// Policy returns the current tunable policy (empty feedback) or optimizes it toward the
	// system goal from feedback/observations. Optional.
	Policy func(feedback string) any
	// Verify returns the evolvable, meta-acceptance-gated verification thresholds and their
	// trust status. Optional.
	Verify func() any
	// Promote runs the system benchmark and cuts a new epoch iff the system improved (the
	// system's own acceptance gate). Returns a start acknowledgement (runs in the background).
	Promote func(name string) any
	// Benchmark returns the last promoted epoch's benchmark baseline. Optional.
	Benchmark func() any
	// Structural flags a cell for the data-structure toolkit and runs one mutation frame on
	// it, returning the outcome — the direct driver for the structural optimizer. Served at
	// /structural?urn=. Optional.
	Structural func(urn string) any
	// BenchmarkReset clears the recorded baseline (use when the benchmark's measure changed so an
	// old baseline is no longer comparable). Served at /benchmark?action=reset. Optional.
	BenchmarkReset func() any
	// Flux returns a cell's source genome — the Flux (cell …) program it was
	// authored in (or its WAT, for a WAT cell) — for read-only display in the
	// console. Served at /flux?urn=. Optional.
	Flux func(urn string) string
}

// Serve starts the always-on localhost listener for the edge services, shutting
// it down when ctx is cancelled. Any nil/absent service is simply not registered.
func Serve(ctx context.Context, addr string, s Services) *http.Server {
	mux := http.NewServeMux()
	if s.Gateway != nil {
		s.Gateway.flow = s.Flow
		mux.Handle("/ingress", s.Gateway)
	}
	if s.Canvas != nil {
		mux.HandleFunc("/canvas", s.Canvas.Page)
		mux.HandleFunc("/canvas/frame", s.Canvas.Frame)
	}
	if s.Build != nil {
		s.Build.baseCtx = ctx // background staged growth outlives the request
		mux.Handle("/build", s.Build)
	}
	if s.Input != nil {
		s.Input.flow = s.Flow
		mux.Handle("/input", s.Input)
	}
	if s.Status != nil {
		mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(s.Status())
		})
	}
	if s.Cells != nil {
		mux.HandleFunc("/cells", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(s.Cells())
		})
	}
	if s.Inspect != nil {
		mux.HandleFunc("/cell", func(w http.ResponseWriter, r *http.Request) {
			urn := r.URL.Query().Get("urn")
			if urn == "" {
				http.Error(w, "urn required", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(s.Inspect(urn))
		})
	}
	if s.Objective != nil {
		mux.HandleFunc("/objective", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"objective": s.Objective()})
		})
	}
	if s.Flux != nil {
		mux.HandleFunc("/flux", func(w http.ResponseWriter, r *http.Request) {
			urn := r.URL.Query().Get("urn")
			if urn == "" {
				http.Error(w, "urn required", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			genome := s.Flux(urn)
			// Pretty-print the s-expression genome (macro-WAT / WAT) for readability,
			// preserving any leading draft marker/comment before the first form.
			if i := strings.IndexByte(genome, '('); i >= 0 {
				genome = genome[:i] + macro.Format(genome[i:])
			}
			_, _ = io.WriteString(w, genome)
		})
	}
	if s.AppMap != nil {
		mux.HandleFunc("/map", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, s.AppMap())
		})
	}
	if s.Plan != nil {
		mux.HandleFunc("/plan", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, s.Plan())
		})
	}
	if s.Walk != nil {
		mux.HandleFunc("/walk", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, s.Walk())
		})
	}
	if s.Log != nil {
		mux.HandleFunc("/log", func(w http.ResponseWriter, r *http.Request) {
			lines := s.Log()
			if n := r.URL.Query().Get("n"); n != "" {
				if k, err := strconv.Atoi(n); err == nil && k > 0 && k < len(lines) {
					lines = lines[len(lines)-k:]
				}
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, strings.Join(lines, "\n"))
		})
	}
	if s.State != nil {
		mux.HandleFunc("/state", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(s.State())
		})
	}
	if s.Perf != nil {
		mux.HandleFunc("/perf", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(s.Perf())
		})
	}
	if s.Vision != nil {
		mux.HandleFunc("/vision", func(w http.ResponseWriter, r *http.Request) {
			q := strings.TrimSpace(r.URL.Query().Get("q"))
			if q == "" {
				q = "Describe what this app is rendering."
			}
			w.Header().Set("Content-Type", "application/json")
			ans, err := s.Vision(q)
			if err != nil {
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"question": q, "answer": ans})
		})
	}
	if s.Mask != nil {
		mux.HandleFunc("/mask", func(w http.ResponseWriter, r *http.Request) {
			on := r.URL.Query().Get("on") != "0"
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"state": s.Mask(on)})
		})
	}
	if s.Feedback != nil {
		mux.HandleFunc("/feedback", func(w http.ResponseWriter, r *http.Request) {
			msg := r.URL.Query().Get("msg")
			if msg == "" {
				if body, _ := io.ReadAll(r.Body); len(body) > 0 {
					msg = string(body)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			if strings.TrimSpace(msg) == "" {
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "empty commentary (use ?msg= or POST body)"})
				return
			}
			_ = json.NewEncoder(w).Encode(s.Feedback(msg))
		})
	}
	if s.Guidance != nil {
		mux.HandleFunc("/guidance", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(s.Guidance())
		})
	}
	if s.OptimizePrompt != nil {
		mux.HandleFunc("/optimize-prompt", func(w http.ResponseWriter, r *http.Request) {
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			fb := r.URL.Query().Get("feedback")
			if fb == "" {
				if body, _ := io.ReadAll(r.Body); len(body) > 0 {
					fb = string(body)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			if name == "" || strings.TrimSpace(fb) == "" {
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "need ?name= and feedback (?feedback= or POST body)"})
				return
			}
			_ = json.NewEncoder(w).Encode(s.OptimizePrompt(name, fb))
		})
	}
	if s.Prompts != nil {
		mux.HandleFunc("/prompts", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(s.Prompts())
		})
	}
	if s.Epoch != nil {
		mux.HandleFunc("/epoch", func(w http.ResponseWriter, r *http.Request) {
			action := strings.TrimSpace(r.URL.Query().Get("action"))
			if action == "" {
				action = "list"
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(s.Epoch(action, strings.TrimSpace(r.URL.Query().Get("name"))))
		})
	}
	if s.Policy != nil {
		mux.HandleFunc("/policy", func(w http.ResponseWriter, r *http.Request) {
			fb := r.URL.Query().Get("feedback")
			if fb == "" {
				if body, _ := io.ReadAll(r.Body); len(body) > 0 {
					fb = string(body)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(s.Policy(fb))
		})
	}
	if s.Verify != nil {
		mux.HandleFunc("/verify", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(s.Verify())
		})
	}
	if s.Promote != nil {
		mux.HandleFunc("/promote", func(w http.ResponseWriter, r *http.Request) {
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			w.Header().Set("Content-Type", "application/json")
			if name == "" {
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "need ?name= for the epoch"})
				return
			}
			_ = json.NewEncoder(w).Encode(s.Promote(name))
		})
	}
	if s.Benchmark != nil {
		mux.HandleFunc("/benchmark", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if strings.TrimSpace(r.URL.Query().Get("action")) == "reset" && s.BenchmarkReset != nil {
				_ = json.NewEncoder(w).Encode(s.BenchmarkReset())
				return
			}
			_ = json.NewEncoder(w).Encode(s.Benchmark())
		})
	}
	if s.Structural != nil {
		mux.HandleFunc("/structural", func(w http.ResponseWriter, r *http.Request) {
			urn := strings.TrimSpace(r.URL.Query().Get("urn"))
			w.Header().Set("Content-Type", "application/json")
			if urn == "" {
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "need ?urn= of the cell to refactor"})
				return
			}
			_ = json.NewEncoder(w).Encode(s.Structural(urn))
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/canvas", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second, // bound header reads (Slowloris) — G112
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second, // vision/build responses can be slow
		IdleTimeout:       120 * time.Second,
	}
	go func() { _ = srv.ListenAndServe() }()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	return srv
}
