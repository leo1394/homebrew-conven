// Optional UI logic checks: node --test internal/runtime/webassets/dashboard.test.cjs
// Node is not required to build or run Conven.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const test = require("node:test");
const html = fs.readFileSync(path.join(__dirname, "index.html"), "utf8");
const script = html.match(/<script>([\s\S]*?)<\/script>/)[1];

test("Guard service log presentation hides only embedded viewer chrome", () => {
  assert.match(script, /if \(guardPanel === "logs"\) \{\s*style.textContent \+= `#logs \.artboard-label,#logs \.section-toggle,#pauseButton,#fullscreenLogsButton\{display:none!important\}`/);
  assert.match(script, /if \(guardPanel === "logs"\) setSectionExpanded\(\$\("#logs"\), true\)/);
  assert.ok(!script.includes('notice.textContent = "HOST'));
  assert.match(html, /id="pauseButton"/);
  assert.match(html, /id="fullscreenLogsButton"/);
});

test("binding rows use identical column tracks regardless of label length", () => {
  const style = html.match(/\.route-row\s*\{([^}]+)\}/)[1];
  assert.match(style, /grid-template-columns: minmax\(0, 1fr\) 16px minmax\(0, 1fr\) minmax\(0, 1\.5fr\)/);
  assert.match(style, /align-items: start/);
  assert.match(html, /\.route-row > span\s*\{[^}]*overflow-wrap: anywhere/);
  assert.match(html, /\.binding\s*\{[^}]*white-space: normal/);
});

class Element {
  constructor(tagName) { this.tagName = tagName; this.children = []; this.attributes = {}; this.dataset = {}; this.value = ""; this.style = {}; this.events = {}; this.hidden = false; this.classes = new Set(); this.classList = { add: (name) => this.classes.add(name), remove: (name) => this.classes.delete(name), toggle: (name, active) => active ? this.classes.add(name) : this.classes.delete(name), contains: (name) => this.classes.has(name) }; }
  setAttribute(key, value) { this.attributes[key] = value; }
  append(...children) { for (const child of children) { if (child.parent) child.remove(); child.parent = this; this.children.push(child); } }
  replaceChildren(...children) { this.children = []; this.append(...children); }
  remove() { this.parent.children = this.parent.children.filter((child) => child !== this); }
  addEventListener(name, handler) { this.events[name] = handler; }
  querySelector(selector) { return this.children.map((child) => (selector === ".panel" && child.className === "panel") || (selector === ".panel-head" && child.className === "panel-head") || (selector === ".panel-title" && child.className === "panel-title") || child.className?.split(" ").includes(selector.slice(1)) ? child : child.querySelector(selector)).find(Boolean); }
  scrollIntoView() { this.scrolled = true; }
  focus() { this.focused = true; }
}

function load(document, overrides = {}) {
  const context = { document, location: { protocol: "file:" }, console, URLSearchParams, setTimeout, clearTimeout, ...overrides };
  vm.createContext(context);
  vm.runInContext(script.replace("      init();", "      globalThis.ui = { state, servingLayout, servingPath, renderTopology, initializeSections, setSectionExpanded, revealSection, setLogsFullscreen, handleLogsFullscreenKey, renderServiceFilters, updateServiceSelection, renderLogs, filteredLogs, resetLogPoll, pollLogs, loadBacklog, renderFailure, renderServiceTable, selectAttempt, renderTimeline, renderSnapshot, bindEvents };"), context);
  return context.ui;
}

test("topology distinguishes local running and remote, with provider-to-consumer arrows", () => {
  const svg = new Element("svg");
  const ui = load({ querySelector: () => svg, createElementNS: (_, tag) => new Element(tag) });
  ui.renderTopology([{ name: "api", state: "running", ports: { http: 8080 } }], [{ consumer: "api", provider: "rpc", mode: "remote" }]);
  const nodes = svg.children.filter((child) => child.tagName === "g");
  assert.match(nodes[0].attributes.class, /running/);
  assert.match(nodes[1].attributes.class, /remote/);
  const edge = svg.children.find((child) => child.tagName === "path");
  assert.equal(edge.children[0].textContent, "rpc serves api");
  assert.match(edge.attributes.class, /remote/);
  assert.equal(svg.children.find((child) => child.tagName === "defs").children[0].attributes.id, "dependencyArrow");
  assert.match(html, /marker-end: url\(#dependencyArrow\)/);
  for (const target of [{ x: 610, y: 82 }, { x: 100, y: 252 }, { x: 100, y: 82 }]) {
    const result = ui.servingPath({ x: 100, y: 82, level: 0 }, { ...target, level: 1 });
    assert.doesNotMatch(result, /NaN|Infinity/);
  }
  assert.notEqual(ui.servingPath({ x: 100, y: 82 }, { x: 610, y: 82 }), ui.servingPath({ x: 610, y: 82 }, { x: 100, y: 82 }));
});

function dashboard(overrides = {}) {
  const elements = new Map();
  const get = (selector) => { if (!elements.has(selector)) elements.set(selector, new Element("div")); return elements.get(selector); };
  const inputs = () => get("#serviceFilters").children.flatMap((chip) => chip.children).filter((child) => child.type === "checkbox");
  const timers = [];
  const ui = load({ querySelector: get, querySelectorAll: (selector) => selector === "#serviceFilters input" ? inputs() : selector === "#serviceFilters input:checked" ? inputs().filter((input) => input.checked) : [], getElementById: () => null, addEventListener: () => {}, createElement: (tag) => new Element(tag), createElementNS: (_, tag) => new Element(tag), createTextNode: (text) => Object.assign(new Element("text"), { textContent: text }) }, { location: { protocol: "http:", hash: "" }, sessionStorage: { getItem: () => "" }, AbortController, IntersectionObserver: class { observe() {} }, window: { setTimeout: () => 0, clearTimeout: () => {} }, setTimeout: (callback) => { timers.push(callback); return timers.length; }, clearTimeout: () => {}, requestAnimationFrame: (callback) => callback(), ...overrides });
  ui.state.snapshot = { sessionToken: "current" };
  ui.renderServiceFilters([{ name: "api" }, { name: "worker" }]);
  return { ui, get, inputs, timers, ids: () => get("#logRows").children.filter((row) => row.className === "log-row").map((row) => row.dataset.id) };
}

test("collected timestamps are labelled and unlevelled output is not an invented severity", () => {
  const { ui, get } = dashboard();
  ui.state.logs = [{ id: "plain", service: "api", text: "Starting server", time: "2026-10-14T00:00:00Z", timeSource: "collected", level: "info", levelSource: "default" }];
  ui.renderLogs();
  const row = get("#logRows").children.find((item) => item.className === "log-row");
  assert.match(row.children[0].textContent, / \*$/);
  assert.match(row.children[0].title, /Collected by Conven/);
  assert.equal(row.children[2].textContent, "INFO");
  assert.match(row.children[2].title, /no log level/);
});

test("HTTP listener buttons copy loopback or LAN URLs and skip RPC", async () => {
  const copied = [];
  const { ui, get } = dashboard({ navigator: { clipboard: { writeText: async (value) => copied.push(value) } } });
  ui.state.snapshot.lanAddress = "192.168.1.8";
  const services = [
    { name: "api", listeners: [{ name: "http", port: 18080, address: "127.0.0.1", mode: "loopback" }] },
    { name: "lan", listeners: [{ name: "http", port: 18084, address: "*", mode: "all-interfaces" }] },
    { name: "rpc", listeners: [{ name: "rpc", port: 18081, address: "127.0.0.1" }] }
  ];
  ui.renderServiceTable(services);
  const rows = get("#serviceTableBody").children;
  let stopped = 0;
  for (const row of rows.slice(0, 2)) await row.children[4].children[0].events.click({ stopPropagation: () => stopped++ });
  assert.deepEqual(copied, ["http://127.0.0.1:18080", "http://192.168.1.8:18084"]);
  assert.equal(stopped, 2);
  assert.equal(rows[2].children[4].children.length, 0);
  ui.state.snapshot.lanAddress = ""; ui.renderServiceTable([services[1]]);
  assert.equal(get("#serviceTableBody").children[0].children[4].children[0].disabled, true);
});

test("attempt defaults collapse success, expand failure, and preserve manual choice on polling", () => {
  const { ui, get } = dashboard();
  const section = get("#attempts"), panel = new Element("div"), body = new Element("div"), button = new Element("button");
  panel.className = "panel"; body.className = "section-body"; button.className = "section-toggle";
  panel.append(body, button); section.append(panel);
  ui.renderTimeline([{ id: "one", status: "succeeded" }]);
  assert.equal(body.hidden, true);
  ui.setSectionExpanded(section, true);
  ui.renderTimeline([{ id: "one", status: "succeeded" }]);
  assert.equal(body.hidden, false);
  ui.setSectionExpanded(section, false);
  ui.renderTimeline([{ id: "two", status: "failed" }]);
  assert.equal(body.hidden, false);
  assert.match(html, /<dt>ENV<\/dt><dd><span id="environmentMeta">/);
});

test("failure count is muted at zero and red only while failures exist", () => {
  const { ui, get } = dashboard();
  ui.state.viewHandled = true;
  for (const [services, count] of [
    [[{ name: "api", state: "running", health: { status: "healthy" } }], 0],
    [[{ name: "api", state: "failed" }], 1],
    [[{ name: "api", state: "running", health: { status: "unhealthy" } }], 1],
    [[], 0]
  ]) {
    ui.renderSnapshot({ sessionToken: "current", services, attempts: [] });
    assert.equal(get("#failedCount").textContent, String(count));
    assert.equal(get("#failedCount").classList.contains("has-failures"), count > 0);
  }
  assert.match(html, /#failedCount\s*\{\s*color: var\(--muted\)/);
  assert.match(html, /#failedCount\.has-failures\s*\{\s*color: var\(--red\)/);
});

test("service selection keeps buffered logs while paused or browsing, including reselect and none", async () => {
  for (const mode of ["paused", "browsing"]) {
    let requests = 0;
    const page = dashboard({ fetch: async () => { requests++; return { ok: true, json: async () => ({ entries: [{ id: "new", service: "api", text: "new line" }], cursor: "new-cursor" }) }; } });
    const { ui, inputs, ids } = page;
    ui.state.paused = mode === "paused"; ui.state.follow = false;
    ui.state.logs = [{ id: "api", service: "api", text: "api line" }, { id: "worker", service: "worker", text: "worker line" }];
    inputs()[1].checked = false; inputs()[1].events.change();
    assert.deepEqual(ids(), ["api"]);
    assert.equal(ui.state.logs.length, 2);
    await ui.pollLogs();
    assert.deepEqual(ids(), ["api", "new"]);
    inputs()[1].checked = true; inputs()[1].events.change();
    assert.deepEqual(ids(), ["api", "worker", "new"]);
    inputs().forEach((input) => input.checked = false); ui.state.attemptFilter = "attempt"; ui.updateServiceSelection();
    assert.deepEqual(ids(), []);
    await ui.pollLogs(); await ui.loadBacklog();
    assert.equal(requests, 1);
  }
});

test("filter refresh renders duplicate or empty fetches without resuming paused live updates", async () => {
  let payload = { entries: [{ id: "api", service: "api", text: "kept" }] };
  const { ui, inputs, ids } = dashboard({ fetch: async () => ({ ok: true, json: async () => payload }) });
  ui.state.paused = true; ui.state.follow = false;
  ui.state.logs = payload.entries;
  inputs()[1].checked = false; inputs()[1].events.change();
  await ui.pollLogs();
  assert.deepEqual(ids(), ["api"]);
  payload = { entries: [{ id: "later", service: "api", text: "arrived while paused" }] };
  await ui.pollLogs();
  assert.deepEqual(ids(), ["api"]);
  assert.equal(ui.state.unseen, 1);
  inputs()[1].checked = true; inputs()[1].events.change();
  payload = { entries: [] };
  await ui.pollLogs();
  assert.deepEqual(ids(), ["api", "later"]);
  assert.equal(ui.state.paused, true);
  assert.equal(ui.state.follow, false);
});

test("late poll and backlog responses cannot replace logs or cursor after selection changes", async () => {
  for (const method of ["pollLogs", "loadBacklog"]) {
    let resolve;
    const { ui, inputs, ids } = dashboard({ fetch: () => new Promise((done) => { resolve = done; }) });
    ui.state.paused = true;
    ui.state.logs = [{ id: "api", service: "api", text: "kept" }];
    const pending = ui[method]();
    inputs()[1].checked = false; inputs()[1].events.change();
    resolve({ ok: true, json: async () => ({ reset: true, entries: [{ id: "stale", service: "api", text: "old selection" }], cursor: "stale" }) });
    await pending;
    assert.deepEqual(ids(), ["api"]);
    assert.equal(ui.state.cursor, "");
    assert.equal(ui.state.logs.length, 1);
  }
});

test("attempt source switches clear rows without attempt metadata while same-source filters retain them", async () => {
  for (const exit of ["toggle", "clearAttempt", "clearFilters", "observation"]) {
    const { ui, get, inputs, ids } = dashboard({ fetch: async () => ({ ok: true, json: async () => ({ entries: [{ id: "archive", service: "api", text: "captured archive" }] }) }) });
    const attempt = { id: "old-attempt", services: ["api"], status: "failed" };
    ui.state.snapshot.attempts = [attempt]; ui.state.snapshot.events = [{ service: "api", status: "running" }];
    ui.state.paused = true; ui.state.logs = [{ id: "live", service: "api", text: "live without attempt field" }];
    ui.bindEvents(); ui.selectAttempt(attempt);
    assert.deepEqual(ids(), []);
    await ui.pollLogs();
    assert.deepEqual(ids(), ["archive"]);
    inputs()[1].checked = true; inputs()[1].events.change();
    get("#logQuery").value = "captured"; get("#logQuery").events.input();
    assert.deepEqual(ids(), ["archive"]);
    if (exit === "toggle") ui.selectAttempt(attempt);
    if (exit === "clearAttempt") get("#clearAttemptButton").events.click();
    if (exit === "clearFilters") get("#clearLogFilters").events.click();
    if (exit === "observation") get("#timeline").children.find((row) => row.tagName === "button").events.click();
    assert.equal(ui.state.attemptFilter, "");
    assert.deepEqual(ids(), []);
    assert.equal(ui.state.logs.length, 0);
  }
});

test("snapshot session or fallback attempt changes clear old sources and reject pending responses", async () => {
  let resolve;
  const { ui, ids } = dashboard({ fetch: () => new Promise((done) => { resolve = done; }) });
  const snapshot = { workspace: "test", environment: "dev", sessionToken: "one", services: [{ name: "api", state: "running" }] };
  ui.state.viewHandled = true; ui.renderSnapshot(snapshot);
  ui.state.paused = true; ui.state.logs = [{ id: "one", service: "api", text: "old session" }];
  ui.renderLogs();
  const pending = ui.pollLogs();
  ui.renderSnapshot({ ...snapshot, sessionToken: "two" });
  assert.deepEqual(ids(), []);
  resolve({ ok: true, json: async () => ({ entries: [{ id: "stale", service: "api", text: "late old session" }] }) });
  await pending;
  assert.equal(ui.state.logs.length, 0);
  ui.renderSnapshot({ ...snapshot, sessionToken: "", attempts: [{ id: "latest-one" }] });
  ui.state.logs = [{ id: "fallback", service: "api", text: "latest archived attempt" }];
  ui.renderSnapshot({ ...snapshot, sessionToken: "", attempts: [{ id: "latest-two" }] });
  assert.equal(ui.state.logs.length, 0);
});

test("failure strip reports current evidence, not historical failures", () => {
  const { ui, get } = dashboard();
  const old = { id: "old", status: "failed", failure: { service: "worker", error: "old worker error" } };
  ui.renderFailure([{ name: "api", state: "running", health: { status: "healthy" } }], [old]);
  assert.equal(get("#failureStrip").classes.has("show"), false);
  ui.renderFailure([{ name: "api", state: "running", health: { status: "unhealthy", message: "current check failed" } }], [old]);
  assert.equal(get("#failureStrip").classes.has("show"), true);
  assert.equal(get("#failureDetail").textContent, "current check failed");
  ui.state.snapshot = {};
  ui.renderFailure([{ name: "api", state: "failed" }], [old]);
  assert.equal(get("#failureStrip").classes.has("show"), true);
  assert.equal(get("#failureTitle").textContent, "old failed");
  assert.equal(get("#failureDetail").textContent, "old worker error");
  ui.renderFailure([], [old]);
  assert.equal(get("#failureDetail").textContent, "old worker error");
  ui.renderFailure([], [{ id: "new", status: "succeeded" }, old]);
  assert.equal(get("#failureStrip").classes.has("show"), false);
  ui.state.snapshot.configurationError = "invalid configuration";
  ui.renderFailure([], []);
  assert.equal(get("#failureDetail").textContent, "invalid configuration");
});

test("no-session unknown health evidence does not mask the matching failed startup error", () => {
  const { ui, get } = dashboard();
  ui.state.snapshot = {};
  const services = [{ name: "api", state: "stopped", health: { status: "unknown", message: "No running session health evidence is available." } }];
  const attempts = [{ id: "latest", status: "failed", failure: { service: "api", error: "bind: address already in use" } }];
  ui.renderFailure(services, attempts);
  assert.equal(get("#failureDetail").textContent, "bind: address already in use");
  services[0].health = { status: "unhealthy", message: "Current listener unavailable" };
  ui.renderFailure(services, attempts);
  assert.equal(get("#failureDetail").textContent, "Current listener unavailable");
});

test("no-session latest startup failure takes priority over other stopped services or global stages", () => {
  const { ui, get } = dashboard();
  ui.state.snapshot = {};
  const services = ["api", "worker"].map((name) => ({ name, state: "stopped", health: { status: "unknown", message: "No running session health evidence is available." } }));
  const attempt = { id: "latest", status: "failed", failure: { service: "worker", stage: "spawn", error: "worker failed to start" } };
  ui.renderFailure(services, [attempt]);
  assert.equal(get("#failureTitle").textContent, "latest failed");
  assert.equal(get("#failureDetail").textContent, "worker failed to start");
  attempt.failure = { stage: "resolve", error: "workspace resolution failed" };
  ui.renderFailure(services, [attempt]);
  assert.equal(get("#failureTitle").textContent, "latest failed");
  assert.equal(get("#failureDetail").textContent, "workspace resolution failed");
  ui.state.snapshot.configurationError = "configuration invalid";
  ui.renderFailure(services, [attempt]);
  assert.equal(get("#failureDetail").textContent, "configuration invalid");
});

test("topology table separates prominent ports from listeners, with multiple and no ports", () => {
  const { ui, get } = dashboard();
  ui.renderServiceTable([{ name: "api", state: "running", pid: 12, ports: { http: 8080, metrics: 9090 }, listeners: [{ name: "http", address: "127.0.0.1", port: 8080 }] }, { name: "worker", state: "running" }]);
  const rows = get("#serviceTableBody").children;
  assert.equal(rows[0].children.length, 5);
  assert.match(rows[0].children[3].textContent, /8080/);
  assert.match(rows[0].children[3].textContent, /9090/);
  assert.doesNotMatch(rows[0].children[3].textContent, /observed|127\.0\.0\.1/);
  assert.match(rows[0].children[4].textContent, /127\.0\.0\.1:8080/);
  assert.equal(rows[1].children[3].textContent, "none reported");
  assert.match(html, /<th scope="col">Port\(s\)<\/th><th scope="col">Listeners<\/th>/);
  assert.match(html, /\.port-list\s*\{[^}]*color: var\(--cyan\)/);
  get("#serviceSearch").value = "missing"; ui.renderServiceTable([]);
  assert.equal(get("#serviceTableBody").children[0].children[0].colSpan, 5);
});

test("consumer state distinguishes shared consumption from configuration guards", () => {
  const { ui, get } = dashboard();
  ui.renderServiceTable([
    { name: "shared", state: "running", consumers: { kafka: { status: "enabled" } } },
    { name: "guarded", state: "running", consumers: { kafka: { status: "disabled" } } },
    { name: "legacy", state: "running" },
    { name: "cli", state: "running", consumers: { kafka: { status: "unrestricted" } } },
    { name: "protected", state: "running", consumers: { kafka: { status: "network-blocked" } } }
  ]);
  const rows = get("#serviceTableBody").children;
  const shared = rows[0].children[2].children[1];
  const guarded = rows[1].children[2].children[1];
  assert.match(shared.textContent, /shared consumption allowed/);
  assert.equal(shared.className, "state warn");
  assert.match(guarded.textContent, /disabled \(guard\)/);
  assert.match(guarded.title, /not runtime-verified/);
  assert.equal(rows[2].children[2].children.length, 1);
  assert.match(rows[3].children[2].children[1].textContent, /shared consumption allowed/);
  assert.equal(rows[3].children[2].children[1].className, "state warn");
  assert.match(rows[4].children[2].children[1].textContent, /network blocked/);
});

test("topology panes share a bounded desktop height and tall graphs scroll inside", () => {
  assert.match(html, /\.topology-layout\s*\{[^}]*height: clamp\(410px, 65vh, 600px\)/);
  assert.match(html, /\.map-area\s*\{[^}]*min-height: 0[^}]*overflow: auto/);
  assert.match(html, /\.config-side\s*\{[^}]*min-height: 0[^}]*overflow: auto/);
  assert.match(html, /\.topology-layout\s*\{[^}]*grid-template-columns: 1fr; height: auto/);
  assert.match(html, /\.map-area, \.config-side\s*\{[^}]*height: clamp\(410px, 65vh, 600px\)/);
  const svg = new Element("svg");
  const ui = load({ querySelector: () => svg, createElementNS: (_, tag) => new Element(tag) });
  const services = Array.from({ length: 20 }, (_, index) => ({ name: `service-${index}`, state: "running" }));
  ui.renderTopology(services, []);
  assert.ok(parseInt(svg.style.height, 10) > 600);
  ui.state.topologyFocus = "service-19"; ui.renderTopology(services, []);
  assert.equal(svg.children.filter((child) => child.tagName === "g").length, 20);
  assert.match(svg.children.find((child) => child.attributes["aria-label"] === "Focus service-19").attributes.class, /selected/);
});

test("shared store-layout provider fans out to two consumers without crossing either node", () => {
  const svg = new Element("svg");
  const ui = load({ querySelector: () => svg, createElementNS: (_, tag) => new Element(tag) });
  const names = ["store-layout-service", "portal-api-service", "sa-api-service"];
  const routes = names.slice(1).map((consumer) => ({ provider: names[0], consumer, mode: "local" }));
  const { positions } = ui.servingLayout(names, routes);
  const provider = positions.get(names[0]), portal = positions.get(names[1]), sa = positions.get(names[2]);
  assert.ok(provider.x < portal.x);
  assert.equal(portal.x, sa.x);
  assert.ok(Math.abs(portal.y - sa.y) >= 90);
  ui.renderTopology(names.map((name) => ({ name, state: "running", ports: {} })), routes);
  const edges = svg.children.filter((child) => child.tagName === "path");
  assert.equal(edges.length, 2);
  assert.notEqual(edges[0].attributes.d.split(" C")[0], edges[1].attributes.d.split(" C")[0]);
  for (let index = 0; index < edges.length; index++) {
    const numbers = edges[index].attributes.d.match(/-?\d+(?:\.\d+)?/g).map(Number);
    // The entire Bézier lies inside the open gap between the provider and consumers.
    for (let coordinate = 0; coordinate < numbers.length; coordinate += 2) {
      assert.ok(numbers[coordinate] > provider.x + 90 && numbers[coordinate] < portal.x - 90);
    }
    assert.equal(edges[index].children[0].textContent, `${names[0]} serves ${names[index + 1]}`);
  }
  const cycle = ui.servingLayout(["a", "b"], [{ provider: "a", consumer: "b" }, { provider: "b", consumer: "a" }]);
  assert.notEqual(cycle.positions.get("a").x, cycle.positions.get("b").x);
});

test("sections collapse without removing content and log navigation expands them", () => {
  const sections = ["overview", "configuration", "logs", "attempts"].map((id) => {
    const section = new Element("section"); section.id = id;
    const panel = new Element("div"); panel.className = "panel";
    const head = new Element("div"); head.className = "panel-head";
    const title = new Element("h2"); title.className = "panel-title"; title.textContent = id;
    head.append(title); panel.append(head, new Element("div")); section.append(panel); return section;
  });
  const ui = load({ querySelectorAll: (selector) => selector === "main section.artboard" ? sections : [], createElement: (tag) => new Element(tag), getElementById: (id) => sections.find((section) => section.id === id) });
  ui.initializeSections();
  for (const section of sections) {
    const button = section.querySelector(".section-toggle"), body = section.querySelector(".section-body");
    assert.equal(button.attributes["aria-expanded"], "true");
    button.events.click(); assert.equal(body.hidden, true); assert.equal(button.attributes["aria-expanded"], "false");
    assert.equal(body.children.length, 1);
  }
  ui.revealSection("logs");
  assert.equal(sections[2].querySelector(".section-body").hidden, false);
  assert.equal(sections[2].scrolled, true);
  assert.match(html, /\.log-frame\s*\{[^}]*height: 75vh/);
});

test("log fullscreen locks outer scrolling, retains position and exits with Escape", () => {
  const panel = new Element("div"), body = new Element("div"), toggle = new Element("button");
  const frame = new Element("div"), button = new Element("button"), root = new Element("html");
  frame.scrollTop = 325; frame.scrollLeft = 17;
  const section = { querySelector: (selector) => ({ ".section-body": body, ".panel": panel, ".section-toggle": toggle })[selector] };
  const elements = { ".logs-panel": panel, "#logFrame": frame, "#logs": section, "#fullscreenLogsButton": button, "#logs .section-toggle": toggle };
  const ui = load({ documentElement: root, querySelector: (selector) => elements[selector] });
  ui.setLogsFullscreen(true);
  assert.ok(panel.classes.has("is-fullscreen"));
  assert.ok(root.classes.has("logs-fullscreen"));
  assert.equal(button.attributes["aria-pressed"], "true");
  assert.equal(toggle.disabled, true);
  assert.equal(body.hidden, false);
  assert.equal(frame.scrollTop, 325);
  let prevented = false;
  assert.equal(ui.handleLogsFullscreenKey({ key: "Escape", preventDefault: () => { prevented = true; } }), true);
  assert.ok(prevented);
  assert.equal(panel.classes.has("is-fullscreen"), false);
  assert.equal(root.classes.has("logs-fullscreen"), false);
  assert.equal(toggle.disabled, false);
  assert.equal(frame.scrollTop, 325);
  assert.equal(frame.scrollLeft, 17);
  assert.equal(button.focused, true);
  assert.match(html, /\.log-frame\s*\{[^}]*overscroll-behavior: contain/);
});
