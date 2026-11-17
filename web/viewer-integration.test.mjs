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

function pcm(generation, sequence, {sampleRate = 48000, channels = 2, sampleFrames = 960} = {}) {
  const data = new ArrayBuffer(64 + sampleFrames * channels * 2);
  const view = new DataView(data);
  view.setUint8(0, 2);
  view.setUint8(1, 1);
  view.setUint16(2, 64, true);
  view.setUint32(4, 5, true);
  view.setBigUint64(8, BigInt(generation), true);
  view.setBigUint64(16, BigInt(sequence), true);
  const durationNS = BigInt(sampleFrames) * 1000000000n / BigInt(sampleRate);
  view.setBigUint64(24, 1234567890123456789n + BigInt(sequence) * durationNS, true);
  view.setUint32(56, sampleFrames, true);
  view.setUint32(60, Number(durationNS), true);
  for (let frame = 0; frame < sampleFrames; frame++) {
    for (let channel = 0; channel < channels; channel++) {
      view.setInt16(64 + (frame * channels + channel) * 2, channel === 0 ? 16384 : -16384, true);
    }
  }
  return data;
}

async function viewer(t, {nativeAudio = false, secureContext = nativeAudio,
  missingWorklet = false, prefixedAudio = false, resumeRequiresRetry = false, audioLoad = null} = {}) {
  const clock = new Clock();
  const window = new Events();
  window.isSecureContext = secureContext;
  const audioContexts = [], audioNodes = [];
  class AudioContext extends Events {
    constructor({sampleRate}) {
      super();
      this.sampleRate = sampleRate;
      this.state = "suspended";
      this.currentTime = 0;
      this.destination = {};
      this.resumeCalls = 0;
      this.closeCalls = 0;
      this.modules = [];
      this.audioWorklet = {addModule: async url => {
        this.modules.push(String(url));
        if (audioLoad !== null) await audioLoad;
      }};
      audioContexts.push(this);
    }
    resume() {
      this.resumeCalls++;
      if (resumeRequiresRetry && this.resumeCalls === 1) {
        return new Promise((resolve, reject) => { this.pendingResume = {resolve, reject}; });
      }
      this.state = "running";
      this.pendingResume?.resolve();
      return Promise.resolve();
    }
    close() {
      this.closeCalls++;
      this.state = "closed";
      this.pendingResume?.reject(new Error("context closed"));
      this.fire("statechange");
      return Promise.resolve();
    }
  }
  class AudioWorkletNode extends Events {
    constructor(context, name, options) {
      super();
      assert.equal(name, "autopaws-audio");
      this.context = context;
      this.options = options;
      this.messages = [];
      this.port = {
        postMessage: (message, transfer) => {
          assert.equal(this.closed, undefined, "a detached audio port received PCM");
          this.messages.push(structuredClone(message, {transfer}));
        },
        close: () => { this.closed = true; },
      };
      audioNodes.push(this);
    }
    connect(destination) { assert.equal(destination, this.context.destination); this.connected = true; }
    disconnect() { this.connected = false; }
    ack(sequence, generation = this.options.processorOptions.generation) {
      this.port.onmessage({data: {type: "ack", generation, sequence}});
    }
  }
  if (nativeAudio) {
    window[prefixedAudio ? "webkitAudioContext" : "AudioContext"] = AudioContext;
    if (!missingWorklet) window.AudioWorkletNode = AudioWorkletNode;
  }
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
    "view-only", "cursor", "scaling", "fullscreen", "toolbar-handle", "capability-list", "capabilities", "fps", "audio"];
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
    WebSocket: Socket, createImageBitmap, performance: {timeOrigin: 1700000000000, now: () => clock.now},
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
    clock, window, document, elements, sockets, decodes, draws, clears, dimensions, audioContexts, audioNodes,
    get socket() { return sockets.at(-1); },
    get sent() { return this.socket.sent; },
    async frame(generation, sequence, payload = Number(sequence), options) {
      await this.socket.message(video(generation, sequence, payload, options));
      await settle();
    },
    async config(audio) {
      await this.socket.message(JSON.stringify({type: "config", audio}));
      await settle();
    },
    async pcm(generation, sequence, options) {
      const data = pcm(generation, sequence, options);
      await this.socket.message(data);
      await settle();
      return data;
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

test("page audio routes negotiated PCM and preserves mute across format changes", async t => {
  const h = await viewer(t, {nativeAudio: true, prefixedAudio: true});
  const button = h.elements.audio;
  const generation = 9007199254740993n;
  assert.equal(button.hidden, true);
  await h.config({status: "available", generation: String(generation), sampleRate: 48000, channels: 2});
  assert.equal(button.hidden, false);
  assert.equal(button.getAttribute("aria-pressed"), "false");
  await h.pcm(generation, 0);
  assert.equal(h.audioContexts.length, 0, "configuration/PCM cannot bypass the enable gesture");
  assert.equal(h.decodes.length, 0, "audio must not enter the JPEG decoder");

  await h.click("audio");
  assert.equal(h.audioContexts.length, 1);
  assert.equal(h.audioContexts[0].sampleRate, 48000);
  assert.match(h.audioContexts[0].modules[0], /\/audio-worklet\.mjs$/);
  assert.equal(button.getAttribute("aria-pressed"), "true");
  assert.match(button.title, /Mute/);
  const wire = await h.pcm(generation, 0);
  const first = h.audioNodes[0];
  assert.deepEqual(first.options.outputChannelCount, [2]);
  assert.equal(first.messages.length, 1);
  assert.equal(first.messages[0].block.generation, generation);
  assert.equal(first.messages[0].block.sequence, 0n);
  assert.deepEqual([...first.messages[0].block.payload], [...new Uint8Array(wire, 64)]);
  await h.pcm(generation, 1);
  assert.equal(first.messages.length, 1, "the page cannot enqueue PCM behind an unacknowledged transfer");
  first.ack(0n);
  await h.pcm(generation, 2);
  assert.equal(first.messages.length, 2);

  await h.click("audio");
  assert.equal(h.audioContexts[0].state, "closed");
  assert.equal(first.closed, true);
  assert.equal(first.connected, false);
  assert.equal(button.getAttribute("aria-pressed"), "false");
  await h.pcm(generation, 3);
  await h.config({status: "available", generation: String(generation + 1n), sampleRate: 96000, channels: 1});
  assert.equal(h.audioContexts.length, 1, "a format change must preserve the mute choice");
  await h.click("audio");
  assert.equal(h.audioContexts[1].sampleRate, 96000);
  const second = h.audioNodes[1];
  assert.deepEqual(second.options.outputChannelCount, [1]);
  await h.pcm(generation, 4);
  assert.equal(second.messages.length, 0, "the prior audio generation cannot reach the replacement graph");
  await h.pcm(generation + 1n, 0, {sampleRate: 96000, channels: 1, sampleFrames: 1920});
  assert.equal(second.messages.length, 1);
  assert.equal(second.messages[0].block.payload.byteLength, 1920 * 2);

  await h.config({status: "off", generation: String(generation + 2n)});
  assert.equal(button.hidden, true);
  assert.equal(h.audioContexts[1].state, "closed");
  await h.config({status: "available", generation: String(generation + 3n), sampleRate: 48000, channels: 2});
  assert.equal(h.audioContexts.length, 3, "returning availability preserves the desired enabled choice");
  assert.equal(button.getAttribute("aria-pressed"), "true");
});

test("same-format page audio recovery keeps playback and orders reset before new-generation PCM", async t => {
  const h = await viewer(t, {nativeAudio: true});
  await h.config({status: "available", generation: "1", sampleRate: 48000, channels: 2});
  await h.click("audio");
  await h.pcm(1, 0);
  const node = h.audioNodes[0];
  await h.config({status: "available", generation: "2", sampleRate: 48000, channels: 2});
  assert.equal(h.audioContexts.length, 1);
  assert.equal(h.audioNodes.length, 1);
  assert.equal(h.audioContexts[0].state, "running");
  assert.equal(h.audioContexts[0].resumeCalls, 1);
  assert.equal(h.audioContexts[0].closeCalls, 0);
  assert.equal(h.elements.audio.getAttribute("aria-pressed"), "true");
  assert.equal(h.elements.audio.textContent, "Audio");
  assert.deepEqual(node.messages[1], {type: "reset", generation: 2n});
  await h.pcm(1, 1);
  await h.pcm(2, 0);
  assert.equal(node.messages.length, 2, "the old transfer still owns admission until acknowledged");
  node.ack(0n, 1n);
  await h.pcm(2, 0);
  assert.equal(node.messages[2].block.generation, 2n);
  node.ack(0n, 1n);
  await h.pcm(2, 1);
  assert.equal(node.messages.length, 3, "late old ACK cannot release the new generation's sequence0 transfer");
  node.ack(0n, 2n);
  await h.pcm(2, 1);
  assert.equal(node.messages[3].block.sequence, 1n);
  assert.equal(h.socket.closes.length, 0);
});

test("page reconnects keep the audio choice while ignoring old-socket PCM and closing graphs", async t => {
  const h = await viewer(t, {nativeAudio: true});
  await h.config({status: "available", generation: "1", sampleRate: 48000, channels: 2});
  await h.click("audio");
  await h.pcm(1, 0);
  const oldSocket = h.socket;
  let resolvePCM;
  const delayed = oldSocket.message({arrayBuffer: () => new Promise(resolve => { resolvePCM = resolve; })});
  oldSocket.close(1006, "reconnect");
  await settle();
  assert.equal(h.audioContexts[0].state, "closed");
  assert.equal(h.audioNodes[0].closed, true);
  assert.equal(h.elements.audio.hidden, true);
  h.clock.advance(500);
  await h.socket.open();
  await h.config({status: "available", generation: "2", sampleRate: 48000, channels: 2});
  assert.equal(h.audioContexts.length, 2);
  assert.equal(h.elements.audio.getAttribute("aria-pressed"), "true");
  resolvePCM(pcm(1, 99));
  await delayed;
  await oldSocket.message(JSON.stringify({type: "config", audio: {status: "off", generation: "999"}}));
  await settle();
  assert.equal(h.audioNodes[1].messages.length, 0, "a delayed old carrier cannot feed the new graph");
  assert.equal(h.elements.audio.hidden, false, "an old carrier cannot replace the new configuration");
  await h.pcm(2, 0);
  assert.equal(h.audioNodes[1].messages.length, 1);

  await h.click("audio");
  h.socket.close(1006, "muted reconnect");
  await settle();
  h.clock.advance(500);
  await h.socket.open();
  await h.config({status: "available", generation: "3", sampleRate: 48000, channels: 2});
  await h.pcm(3, 0);
  assert.equal(h.audioContexts.length, 2, "reconnect must not silently unmute");
  assert.equal(h.elements.audio.getAttribute("aria-pressed"), "false");
  await h.click("audio");
  assert.equal(h.audioContexts.length, 3);
  await h.window.fire("pagehide").done;
  await settle();
  assert.ok(h.audioContexts.every(context => context.state === "closed"));
  assert.ok(h.audioNodes.every(node => node.closed && !node.connected));
  h.clock.advance(10000);
  assert.equal(h.sockets.length, 3);
});

test("audio capability failures leave page video and keyboard/mouse input usable", async t => {
  for (const options of [{nativeAudio: true, missingWorklet: true}, {nativeAudio: true, secureContext: false}]) {
    await t.test(JSON.stringify(options), async t => {
      const h = await viewer(t, options);
      await h.show();
      await h.config({status: "available", generation: "1", sampleRate: 48000, channels: 2});
      assert.equal(h.elements.audio.hidden, false);
      await h.click("audio");
      assert.equal(h.elements.audio.getAttribute("aria-pressed"), "false");
      assert.equal(h.elements.capabilities.hidden, false);
      assert.equal(h.elements.capabilities.open, true);
      assert.ok(h.elements["capability-list"].children.some(item => /AudioWorklet.*secure context/.test(item.textContent)));
      assert.equal(h.audioContexts.length, 0);
      await h.pcm(1, 0);
      await h.show(1, 2);
      h.key("keydown", "KeyA");
      h.key("keyup", "KeyA");
      h.mouse("mousedown", {buttons: 1});
      h.mouse("mouseup");
      assert.equal(h.draws.length, 2);
      assert.deepEqual(h.sent, [
        {type: "keydown", code: "KeyA"}, {type: "keyup", code: "KeyA"},
        {type: "mousedown", x: 0, y: 0, buttons: 1}, {type: "mouseup", x: 0, y: 0, buttons: 0},
      ]);
      assert.equal(h.socket.closes.length, 0, "an audio-only capability failure cannot close the shared stream");
      await h.config({status: "unavailable", generation: "2"});
      assert.equal(h.elements.audio.hidden, true);
    });
  }
});

test("the page audio button retries enabled but pending playback during a fresh gesture", async t => {
  const h = await viewer(t, {nativeAudio: true, resumeRequiresRetry: true});
  await h.config({status: "available", generation: "1", sampleRate: 48000, channels: 2});
  await h.click("audio");
  assert.equal(h.audioContexts[0].resumeCalls, 1);
  assert.equal(h.audioContexts[0].state, "suspended");
  assert.equal(h.elements.audio.getAttribute("aria-pressed"), "false");
  assert.match(h.elements.audio.title, /again/);
  await h.pcm(1, 0);
  assert.equal(h.audioNodes.length, 0);
  await h.click("audio");
  assert.equal(h.audioContexts.length, 1);
  assert.equal(h.audioContexts[0].resumeCalls, 2);
  assert.equal(h.elements.audio.getAttribute("aria-pressed"), "true");
  await h.pcm(1, 1);
  assert.equal(h.audioNodes[0].messages.length, 1);
});


test("a second page audio click cancels module loading and late completion stays muted", async t => {
  let finishLoad;
  const audioLoad = new Promise(resolve => { finishLoad = resolve; });
  const h = await viewer(t, {nativeAudio: true, audioLoad});
  await h.config({status: "available", generation: "1", sampleRate: 48000, channels: 2});
  await h.click("audio");
  assert.equal(h.audioContexts.length, 1);
  assert.equal(h.audioContexts[0].state, "running");
  assert.equal(h.audioNodes.length, 0);
  assert.equal(h.elements.audio.textContent, "Cancel audio");
  assert.equal(h.elements.audio.getAttribute("aria-pressed"), "false");
  await h.click("audio");
  assert.equal(h.audioContexts[0].state, "closed");
  finishLoad();
  await settle();
  assert.equal(h.audioContexts.length, 1, "cancel must not start another audio context");
  assert.equal(h.audioNodes.length, 0, "late module completion cannot unmute or connect a worklet");
  assert.equal(h.elements.audio.getAttribute("aria-pressed"), "false");
  assert.equal(h.elements.audio.textContent, "Audio");
  await h.pcm(1, 0);
  assert.equal(h.audioNodes.length, 0);
});
