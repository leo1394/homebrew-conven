// Optional UI logic checks: node --test internal/runtime/webassets/dashboard.test.cjs
// Node is not required to build or run Conven.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const test = require("node:test");
const html = fs.readFileSync(path.join(__dirname, "index.html"), "utf8");
const script = html.match(/<script>([\s\S]*?)<\/script>/)[1];

test("binding rows use identical column tracks regardless of label length", () => {
  const style = html.match(/\.route-row\s*\{([^}]+)\}/)[1];
  assert.match(style, /grid-template-columns: minmax\(0, 1fr\) 16px minmax\(0, 1fr\) minmax\(0, 1\.5fr\)/);
  assert.match(style, /align-items: start/);
  assert.match(html, /\.route-row > span\s*\{[^}]*overflow-wrap: anywhere/);
  assert.match(html, /\.binding\s*\{[^}]*white-space: normal/);
});

class Element {
  constructor(tagName) { this.tagName = tagName; this.children = []; this.attributes = {}; this.style = {}; this.events = {}; this.hidden = false; this.classes = new Set(); this.classList = { toggle: (name, active) => active ? this.classes.add(name) : this.classes.delete(name), contains: (name) => this.classes.has(name) }; }
  setAttribute(key, value) { this.attributes[key] = value; }
  append(...children) { for (const child of children) { if (child.parent) child.remove(); child.parent = this; this.children.push(child); } }
  remove() { this.parent.children = this.parent.children.filter((child) => child !== this); }
  addEventListener(name, handler) { this.events[name] = handler; }
  querySelector(selector) { return this.children.map((child) => (selector === ".panel" && child.className === "panel") || (selector === ".panel-head" && child.className === "panel-head") || (selector === ".panel-title" && child.className === "panel-title") || child.className?.split(" ").includes(selector.slice(1)) ? child : child.querySelector(selector)).find(Boolean); }
  scrollIntoView() { this.scrolled = true; }
  focus() { this.focused = true; }
}

function load(document) {
  const context = { document, location: { protocol: "file:" }, console, URLSearchParams, setTimeout, clearTimeout };
  vm.createContext(context);
  vm.runInContext(script.replace("      init();", "      globalThis.ui = { servingLayout, servingPath, renderTopology, initializeSections, setSectionExpanded, revealSection, setLogsFullscreen, handleLogsFullscreenKey };"), context);
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
