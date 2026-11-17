import assert from "node:assert/strict";
import test from "node:test";

// Exercise the shipped entry module, including event wiring and asynchronous
// image ownership. This is deliberately a small DOM, not another input/frame
// reducer: observable results are WebSocket messages, canvas draws and controls.
class Events {
  listeners = new Map();
  parent = null;
  addEventListener(type, listener, options) {
    const listeners = this.listeners.get(type) ?? [];
    listeners.push({listener, options});
    this.listeners.set(type, listeners);
  }
  fire(type, fields = {}, bubbles = false) {
    const event = {
      type, target: this, currentTarget: null, defaultPrevented: false,
      preventDefault() { this.defaultPrevented = true; },
      ...fields,
    };
    const promises = [];
    for (let target = this; target !== null; target = bubbles ? target.parent : null) {
      event.currentTarget = target;
      for (const {listener} of target.listeners.get(type) ?? []) {
        const result = listener(event);
        if (result?.then) promises.push(result);
      }
    }
    return {event, done: Promise.all(promises)};
  }
}

class Classes {
  values = new Set();
  add(value) { this.values.add(value); }
  remove(value) { this.values.delete(value); }
  contains(value) { return this.values.has(value); }
  toggle(value, force = !this.values.has(value)) {
    if (force) this.add(value);
    else this.remove(value);
    return force;
  }
}

class Element extends Events {
  style = {};
  classList = new Classes();
  attributes = new Map();
  children = [];
  hidden = false;
  disabled = false;
  textContent = "";
  offsetHeight = 44;
  offsetWidth = 800;
  clientWidth = 960;
  clientHeight = 600;
  constructor(document, id = "") { super(); this.document = document; this.id = id; }
  setAttribute(name, value) { this.attributes.set(name, value); }
  getAttribute(name) { return this.attributes.get(name) ?? null; }
  replaceChildren(...children) { this.children = children; }
  contains(element) {
    for (let target = element; target; target = target.parent) if (target === this) return true;
    return false;
  }
  matches() { return false; }
  setPointerCapture() {}
  focus() {
    if (this.document.activeElement === this) return;
    const previous = this.document.activeElement;
    this.document.activeElement = this;
    previous?.fire("blur");
    this.fire("focus");
  }
  getBoundingClientRect() {
    return this.rect ?? {
      left: 100, top: 70,
      width: Number.parseFloat(this.style.width) || this.offsetWidth,
      height: Number.parseFloat(this.style.height) || this.offsetHeight,
    };
  }
}

class Clock {
  now = 0;
  next = 1;
  timers = new Map();
  schedule(callback, delay = 0, interval = false) {
    const id = this.next++;
    this.timers.set(id, {callback, at: this.now + Math.max(0, delay), interval, delay});
    return id;
  }
  advance(ms) {
    const until = this.now + ms;
    for (;;) {
      let selected = null;
      for (const [id, timer] of this.timers) {
        if (timer.at <= until && (selected === null || timer.at < selected[1].at)) selected = [id, timer];
      }
      if (selected === null) break;
      const [id, timer] = selected;
      this.now = timer.at;
      if (timer.interval) timer.at += timer.delay;
      else this.timers.delete(id);
      timer.callback();
    }
    this.now = until;
  }
}

const settle = async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); };
let instance = 0;

function video(generation, sequence, payload, {
  width = 1280, height = 800, crop = {x: 0, y: 40, width: 1280, height: 720},
} = {}) {
  const data = new ArrayBuffer(65);
  const view = new DataView(data);
  view.setUint8(0, 1);
  view.setUint8(1, 1);
  view.setUint16(2, 64, true);
  view.setBigUint64(8, BigInt(generation), true);
  view.setBigUint64(16, BigInt(sequence), true);
  view.setUint32(32, width, true);
  view.setUint32(36, height, true);
  for (const [offset, key] of [[40, "x"], [44, "y"], [48, "width"], [52, "height"]]) {
    view.setUint32(offset, crop[key], true);
  }
  view.setUint8(64, payload);
  return data;
}

async function viewer(t) {
  const clock = new Clock();
  const window = new Events();
  window.isSecureContext = false;
  const document = new Events();
  document.parent = window;
  document.hidden = false;
  document.hasFocus = () => true;
  document.fullscreenElement = null;
  document.fullscreenEnabled = false;
  document.body = new Element(document, "body");
  document.body.parent = document;
  document.documentElement = new Element(document, "html");
  document.activeElement = document.body;
  document.createElement = () => new Element(document);
  const ids = ["toolbar", "viewport", "screen", "message", "status", "ctrl-alt-del", "alt-tab",
    "view-only", "cursor", "scaling", "fullscreen", "toolbar-handle", "capability-list", "capabilities", "fps"];
  const elements = Object.fromEntries(ids.map(id => [id, new Element(document, id)]));
  for (const element of Object.values(elements)) element.parent = document.body;
  elements.screen.parent = elements.viewport;
  document.getElementById = id => {
    assert.ok(elements[id], `unexpected element ${id}`);
    return elements[id];
  };
  const draws = [];
  const clears = [];
  const dimensions = [];
  let width = 1, height = 1;
  Object.defineProperties(elements.screen, {
    width: {get: () => width, set: value => { width = value; dimensions.push(["width", value]); }},
    height: {get: () => height, set: value => { height = value; dimensions.push(["height", value]); }},
  });
  elements.screen.getContext = () => ({
    clearRect: (...args) => clears.push(args),
    drawImage: (...args) => draws.push(args),
  });
  const sockets = [];
  class Socket extends Events {
    static OPEN = 1;
    readyState = 0;
    bufferedAmount = 0;
    sent = [];
    closes = [];
    constructor(url) { super(); this.url = String(url); sockets.push(this); }
    open() { this.readyState = 1; return this.fire("open").done; }
    message(data) { return this.fire("message", {data}).done; }
    send(data) {
      assert.equal(this.readyState, 1, "browser sent after WebSocket stopped accepting writes");
      this.sent.push(JSON.parse(data));
    }
    close(code = 1000, reason = "") {
      if (this.readyState >= 2) return;
      this.closes.push({code, reason});
      this.readyState = 2;
      queueMicrotask(() => { this.readyState = 3; this.fire("close"); });
    }
  }
  const decodes = [];
  const createImageBitmap = blob => new Promise((resolve, reject) => {
    decodes.push({blob, resolve, reject, settled: false});
  });
  const globals = {
    window, document, navigator: {}, location: new URL("http://127.0.0.1:9999/"),
    WebSocket: Socket, createImageBitmap, performance: {now: () => clock.now},
    setTimeout: (callback, delay) => clock.schedule(callback, delay),
    clearTimeout: id => clock.timers.delete(id),
    setInterval: (callback, delay) => clock.schedule(callback, delay, true),
    clearInterval: id => clock.timers.delete(id),
    innerWidth: 1200, innerHeight: 800, ResizeObserver: undefined,
  };
  const saved = new Map(Object.keys(globals).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]));
  for (const [key, value] of Object.entries(globals)) {
    Object.defineProperty(globalThis, key, {value, configurable: true, writable: true});
  }
  t.after(async () => {
    window.fire("pagehide");
    await settle();
    for (const decode of decodes) if (!decode.settled) {
      decode.settled = true;
      decode.reject(new Error("test finished"));
    }
    await settle();
    for (const [key, descriptor] of saved) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor);
      else delete globalThis[key];
    }
  });
  await import(`./assets/viewer.mjs?integration=${++instance}`);
  assert.equal(sockets.length, 1);
  await sockets[0].open();
  const h = {
    clock, window, document, elements, sockets, decodes, draws, clears, dimensions,
    get socket() { return sockets.at(-1); },
    get sent() { return this.socket.sent; },
    async frame(generation, sequence, payload = Number(sequence), options) {
      await this.socket.message(video(generation, sequence, payload, options));
      await settle();
    },
    async image(index = decodes.length - 1, options = {}) {
      const decode = decodes[index];
      assert.ok(decode && !decode.settled, `decode ${index} must be pending`);
      decode.settled = true;
      const bitmap = {width: 1280, height: 800, closed: false, ...options,
        close() { this.closed = true; }};
      decode.resolve(bitmap);
      await settle();
      return bitmap;
    },
    async badImage(index = decodes.length - 1) {
      decodes[index].settled = true;
      decodes[index].reject(new Error("unreadable JPEG"));
      await settle();
    },
    mouse(type, {target = elements.screen, clientX = 100, clientY = 70, buttons = 0, ...fields} = {}) {
      return target.fire(type, {clientX, clientY, buttons, ...fields}, true).event;
    },
    key(type, code, repeat = false) { return elements.viewport.fire(type, {code, repeat}, true).event; },
    async click(id) { await elements[id].fire("click", {}, true).done; await settle(); },
    async visible(visible) { document.hidden = !visible; await document.fire("visibilitychange").done; await settle(); },
    async focus(focused) { await window.fire(focused ? "focus" : "blur").done; await settle(); },
    async show(generation = 1, sequence = 1) { await this.frame(generation, sequence); return this.image(); },
  };
  return h;
}

test("new generation releases a held mouse before slow or failed replacement JPEGs", async t => {
  const h = await viewer(t);
  await h.show();
  h.mouse("mousedown", {buttons: 1});
  assert.deepEqual(h.sent, [{type: "mousedown", x: 0, y: 0, buttons: 1}]);

  await h.frame(2, 2);
  assert.deepEqual(h.sent.at(-1), {type: "release_all"});
  assert.equal(h.elements.screen.width, 1);
  h.mouse("mouseup", {buttons: 0});
  await h.badImage();
  assert.equal(h.draws.length, 1, "failed replacement must not restore the prior frame");
  assert.match(h.elements.message.textContent, /readable/);
  assert.equal(h.sent.length, 2, "a late physical up cannot reacquire released ownership");

  await h.show(2, 3);
  h.mouse("mousedown", {buttons: 1});
  h.mouse("mouseup", {buttons: 0, clientX: 1059, clientY: 609});
  assert.deepEqual(h.sent.slice(-2), [
    {type: "mousedown", x: 0, y: 0, buttons: 1},
    {type: "mouseup", x: 4095, y: 4095, buttons: 0},
  ]);
});

test("mouseup with temporarily empty layout releases ownership without coordinates", async t => {
  const h = await viewer(t);
  await h.show();
  h.key("keydown", "ControlLeft");
  h.mouse("mousedown", {buttons: 1});
  h.elements.screen.rect = {left: 100, top: 70, width: 0, height: 540};
  h.mouse("mouseup");
  assert.deepEqual(h.sent.at(-1), {type: "release_all"});
  h.key("keyup", "ControlLeft");
  assert.equal(h.sent.length, 3, "fallback release also forgets any held key");
});

test("normal mouseup preserves modifiers and every mouse-button mask transition", async t => {
  const h = await viewer(t);
  await h.show();
  h.mouse("pointerdown", {pointerId: 3});
  assert.equal(h.document.activeElement, h.elements.viewport);
  h.key("keydown", "ControlLeft");
  h.key("keydown", "ControlLeft", true);
  h.mouse("mousedown", {buttons: 1});
  h.mouse("mousedown", {buttons: 3});
  h.mouse("mouseup", {buttons: 1, target: h.document.body});
  h.mouse("mouseup", {buttons: 0, target: h.document.body});
  h.key("keyup", "ControlLeft");
  assert.deepEqual(h.sent, [
    {type: "keydown", code: "ControlLeft"},
    {type: "mousedown", x: 0, y: 0, buttons: 1},
    {type: "mousedown", x: 0, y: 0, buttons: 3},
    {type: "mouseup", x: 0, y: 0, buttons: 1},
    {type: "mouseup", x: 0, y: 0, buttons: 0},
    {type: "keyup", code: "ControlLeft"},
  ]);
});

test("slow same-generation decoding retains usable coordinates and rejects dimension mismatches", async t => {
  const h = await viewer(t);
  await h.show();
  const assignments = h.dimensions.length;
  h.mouse("mousedown", {buttons: 1});
  await h.frame(1, 2);
  h.mouse("mouseup", {clientX: 1059, clientY: 609});
  assert.deepEqual(h.sent.at(-1), {type: "mouseup", x: 4095, y: 4095, buttons: 0});
  const bad = await h.image(1, {width: 800, height: 1280});
  assert.equal(bad.closed, true);
  assert.equal(h.draws.length, 1);
  assert.equal(h.dimensions.length, assignments);
  assert.equal(h.elements.message.hidden, true, "a rejected same-generation frame leaves the good image visible");
  await h.show(1, 3);
  assert.equal(h.draws.length, 2);
  assert.equal(h.dimensions.length, assignments, "same-size draws must not reset canvas backing storage");
});

test("one decode plus one newest pending JPEG survives a continuously arriving stream", async t => {
  const h = await viewer(t);
  await h.frame(1, 1, 1);
  for (let sequence = 2; sequence <= 100; sequence++) await h.frame(1, sequence, sequence);
  assert.equal(h.decodes.length, 1, "incoming frames cannot create concurrent image decoders");
  const first = await h.image(0);
  assert.equal(first.closed, true);
  assert.equal(h.draws.length, 1, "continuous same-generation input must not starve the in-flight decode");
  assert.equal(h.decodes.length, 2);
  assert.deepEqual([...new Uint8Array(await h.decodes[1].blob.arrayBuffer())], [100]);
  await h.image(1);
  assert.equal(h.draws.length, 2);
  await h.frame(0, 101);
  await h.frame(1, 99);
  assert.equal(h.decodes.length, 2, "late generation or sequence must not start decoding");
  assert.deepEqual(h.draws[1].slice(1), [0, 40, 1280, 720, 0, 0, 1280, 720]);
});

test("a newer generation invalidates an in-flight decode before it can draw", async t => {
  const h = await viewer(t);
  await h.show();
  await h.frame(1, 2, 2);
  await h.frame(2, 3, 3);
  await h.frame(1, 4, 4);
  const stale = await h.image(1);
  assert.equal(stale.closed, true);
  assert.equal(h.draws.length, 1);
  assert.equal(h.decodes.length, 3);
  assert.deepEqual([...new Uint8Array(await h.decodes[2].blob.arrayBuffer())], [3]);
  await h.image(2);
  assert.equal(h.draws.length, 2);
  assert.equal(h.elements.screen.width, 1280);
});

test("reconnect ignores old image and delayed Blob work, with no held input replay", async t => {
  const h = await viewer(t);
  await h.show();
  h.key("keydown", "ShiftLeft");
  await h.frame(1, 2);
  const oldSocket = h.socket;
  let resolveBuffer;
  const delayed = oldSocket.message({arrayBuffer: () => new Promise(resolve => { resolveBuffer = resolve; })});
  oldSocket.close(1006, "network lost");
  await settle();
  h.clock.advance(499);
  assert.equal(h.sockets.length, 1);
  h.clock.advance(1);
  assert.equal(h.sockets.length, 2);
  await h.socket.open();
  h.key("keyup", "ShiftLeft");
  assert.deepEqual(h.sent, []);
  resolveBuffer(video(99, 99, 99));
  await delayed;
  await h.frame(1, 1, 7);
  const stale = await h.image(1);
  assert.equal(stale.closed, true);
  assert.equal(h.draws.length, 1, "old connection's decoder must not repaint");
  assert.equal(h.decodes.length, 3, "old delayed Blob must not enqueue any decoder");
  assert.deepEqual([...new Uint8Array(await h.decodes[2].blob.arrayBuffer())], [7]);
  await h.image(2);
  assert.equal(h.draws.length, 2);
  assert.equal(h.elements.status.textContent, "Connected");
});

test("view-only, focus and visibility gates release only owned state and gate shortcuts", async t => {
  const h = await viewer(t);
  await h.show();
  await h.click("ctrl-alt-del");
  await h.click("alt-tab");
  assert.deepEqual(h.sent, [
    {type: "shortcut", keys: ["ControlLeft", "AltLeft", "Delete"]},
    {type: "shortcut", keys: ["AltLeft", "Tab"]},
  ]);
  h.key("keydown", "KeyA");
  await h.click("view-only");
  assert.deepEqual(h.sent.at(-1), {type: "release_all"});
  assert.equal(h.elements["ctrl-alt-del"].disabled, true);
  const passiveCount = h.sent.length;
  assert.equal(h.key("keydown", "KeyB").defaultPrevented, false);
  h.mouse("mousedown", {buttons: 1});
  await h.click("ctrl-alt-del");
  await h.focus(false);
  await h.visible(false);
  await h.focus(true);
  await h.visible(true);
  assert.equal(h.sent.length, passiveCount);
  await h.click("view-only");
  assert.equal(h.elements["ctrl-alt-del"].disabled, false);
  h.key("keyup", "KeyA");
  assert.equal(h.sent.length, passiveCount, "enabling input cannot resurrect released keys");

  h.key("keydown", "KeyC");
  await h.focus(false);
  assert.deepEqual(h.sent.at(-1), {type: "release_all"});
  const blurredCount = h.sent.length;
  await h.click("alt-tab");
  await h.focus(false);
  assert.equal(h.sent.length, blurredCount);
  await h.focus(true);
  h.mouse("mousedown", {buttons: 1});
  await h.visible(false);
  assert.deepEqual(h.sent.at(-1), {type: "release_all"});
  const hiddenCount = h.sent.length;
  await h.click("alt-tab");
  await h.visible(false);
  assert.equal(h.sent.length, hiddenCount);
  await h.visible(true);
  h.mouse("mouseup");
  assert.equal(h.sent.length, hiddenCount);
});

test("local focus loss, pointer cancellation and pagehide release held input once", async t => {
  const h = await viewer(t);
  await h.show();
  h.mouse("pointerdown", {pointerId: 1});
  h.key("keydown", "ControlLeft");
  h.elements.cursor.focus();
  assert.deepEqual(h.sent.at(-1), {type: "release_all"});
  h.elements.cursor.focus();
  assert.equal(h.sent.length, 2);
  h.mouse("pointerdown", {pointerId: 2});
  h.mouse("mousedown", {buttons: 1});
  h.mouse("pointercancel", {pointerId: 2});
  h.mouse("pointercancel", {pointerId: 2});
  assert.deepEqual(h.sent.slice(-2), [
    {type: "mousedown", x: 0, y: 0, buttons: 1}, {type: "release_all"},
  ]);
  h.key("keydown", "AltLeft");
  await h.window.fire("pagehide").done;
  await settle();
  assert.deepEqual(h.sent.at(-1), {type: "release_all"});
  assert.deepEqual(h.socket.closes.at(-1), {code: 1000, reason: "Viewer closing"});
  h.clock.advance(10000);
  assert.equal(h.sockets.length, 1, "a page being left must not reconnect");
});

test("discrete input flushes the latest move, wheel is nonpassive, passive mode stays local", async t => {
  const h = await viewer(t);
  await h.show();
  assert.equal(h.elements.screen.listeners.get("wheel")[0].options.passive, false);
  h.mouse("mousemove", {clientX: 200});
  h.mouse("mousemove", {clientX: 1059, clientY: 609});
  h.key("keydown", "KeyA");
  assert.deepEqual(h.sent, [
    {type: "mousemove", x: 4095, y: 4095, buttons: 0},
    {type: "keydown", code: "KeyA"},
  ]);
  const wheel = h.mouse("wheel", {deltaY: 1});
  assert.equal(wheel.defaultPrevented, true);
  assert.deepEqual(h.sent.at(-1), {type: "scroll", deltaY: -3});
  assert.equal(h.mouse("contextmenu").defaultPrevented, true);
  await h.click("view-only");
  const count = h.sent.length;
  assert.equal(h.mouse("wheel", {deltaY: -1}).defaultPrevented, false);
  assert.equal(h.mouse("contextmenu").defaultPrevented, false);
  h.clock.advance(100);
  assert.equal(h.sent.length, count);
});
