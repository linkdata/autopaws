import assert from "node:assert/strict";
import test from "node:test";
import {mapPointer, decodeMediaMessage, createFrameGuard, createInputController}
  from "./assets/viewer-helpers.mjs";

function clock() {
  let time = 0;
  let nextID = 0;
  const timers = new Map();
  return {
    now: () => time,
    schedule(callback, delay) {
      const id = nextID++;
      timers.set(id, {callback, due: time + delay});
      return id;
    },
    cancel(id) { timers.delete(id); },
    get pending() { return timers.size; },
    advance(duration) {
      const end = time + duration;
      for (;;) {
        const first = [...timers].sort((a, b) => a[1].due - b[1].due)[0];
        if (!first || first[1].due > end) break;
        time = first[1].due;
        timers.delete(first[0]);
        first[1].callback();
      }
      time = end;
    },
    // Let a browser animation frame run before a due timeout callback.
    jump(duration) { time += duration; },
  };
}

function input(options = {}) {
  const time = clock();
  const messages = [];
  const controller = createInputController({
    ...time,
    send(message) { messages.push(structuredClone(message)); },
    ...options,
  });
  controller.setConnected(true);
  return {controller, time, messages};
}

function video(overrides = {}) {
  return {
    kind: 1, generation: 1n, sequence: 1n,
    timestampFlags: 3, timestampNS: 1234567890123456789n,
    width: 1920, height: 1080,
    crop: {x: 0, y: 140, width: 1920, height: 800},
    ...overrides,
  };
}

function media({kind = 1, flags = 3, generation = 9007199254740993n,
  sequence = 9007199254740995n, timestamp = 1234567890123456789n,
  width = 1920, height = 1080, crop = [0, 140, 1920, 800],
  sampleFrames = 0, durationNS = 0, payload = [0xff, 0xd8, 0xff, 0xd9]} = {}) {
  const buffer = new ArrayBuffer(64 + payload.length);
  const view = new DataView(buffer);
  view.setUint8(0, kind);
  view.setUint8(1, 1);
  view.setUint16(2, 64, true);
  view.setUint32(4, flags, true);
  view.setBigUint64(8, generation, true);
  view.setBigUint64(16, sequence, true);
  view.setBigUint64(24, timestamp, true);
  [width, height, ...crop, sampleFrames, durationNS]
    .forEach((value, index) => view.setUint32(32 + index * 4, value, true));
  new Uint8Array(buffer, 64).set(payload);
  return buffer;
}

test("letterboxed visible pixel corners map to exact HID endpoints after CSS scaling", () => {
  // Only the 1920x800 crop is displayed, without its source y=140 border.
  const rect = {left: 100, top: 70, width: 960, height: 400};
  const lastX = rect.left + rect.width - 1;
  const lastY = rect.top + rect.height - 1;
  assert.deepEqual(mapPointer(100, 70, rect, 1920, 800), {x: 0, y: 0});
  assert.deepEqual(mapPointer(lastX, 70, rect, 1920, 800), {x: 4095, y: 0});
  assert.deepEqual(mapPointer(100, lastY, rect, 1920, 800), {x: 0, y: 4095});
  assert.deepEqual(mapPointer(lastX, lastY, rect, 1920, 800), {x: 4095, y: 4095});
  assert.deepEqual(mapPointer(-100, 10000, rect, 1920, 800), {x: 0, y: 4095});
  assert.deepEqual(mapPointer(1060, 470, rect, 1920, 800), {x: 4095, y: 4095});
});

test("upscaled and fractional display bounds retain reachable CSS pixel endpoints", () => {
  for (const rect of [
    {left: 10, top: 20, width: 3840, height: 1600},
    {left: 10.25, top: 20.5, width: 960.5, height: 400.25},
  ]) {
    assert.deepEqual(mapPointer(rect.left, rect.top, rect, 1920, 800), {x: 0, y: 0});
    assert.deepEqual(mapPointer(rect.left + rect.width - 1, rect.top + rect.height - 1,
      rect, 1920, 800), {x: 4095, y: 4095});
    assert.deepEqual(mapPointer(rect.left + (rect.width - 1) / 2, rect.top + (rect.height - 1) / 2,
      rect, 1920, 800), {x: 2048, y: 2048});
  }
});

test("a one-pixel visible axis maps to zero", () => {
  const rect = {left: 0, top: 0, width: 100, height: 100};
  assert.deepEqual(mapPointer(100, 100, rect, 1, 1), {x: 0, y: 0});
  assert.deepEqual(mapPointer(100, 100, rect, 1, 2), {x: 0, y: 4095});
  assert.deepEqual(mapPointer(100, 100, rect, 2, 1), {x: 4095, y: 0});
  assert.deepEqual(mapPointer(100, 100, {...rect, width: 1}, 100, 100), {x: 0, y: 4095});
  assert.deepEqual(mapPointer(100, 100, {...rect, height: 0.5}, 100, 100), {x: 4095, y: 0});
});

test("coordinate mapping tolerates missing layout without emitting invalid wire numbers", () => {
  const rect = {left: 0, top: 0, width: 100, height: 100};
  for (const args of [
    [0, 0, null, 1, 1], [NaN, 0, rect, 1, 1], [0, Infinity, rect, 1, 1],
    [0, 0, {...rect, width: 0}, 1, 1], [0, 0, {...rect, height: -1}, 1, 1],
    [0, 0, rect, 0, 1], [0, 0, rect, 1, 0.5], [0, 0, rect, Infinity, 1],
  ]) assert.equal(mapPointer(...args), null);
});

test("wire parser preserves all uint64 bits and returns an unchanged payload view", () => {
  const buffer = media();
  const decoded = decodeMediaMessage(buffer);
  assert.equal(decoded.kind, 1);
  assert.equal(decoded.generation, 9007199254740993n);
  assert.equal(decoded.sequence, 9007199254740995n);
  assert.equal(decoded.timestampNS, 1234567890123456789n);
  assert.equal(decoded.timestampFlags, 3);
  assert.deepEqual(decoded.crop, {x: 0, y: 140, width: 1920, height: 800});
  assert.equal(decoded.width, 1920);
  assert.equal(decoded.height, 1080);
  assert.equal(decoded.sampleFrames, 0);
  assert.equal(decoded.durationNS, 0);
  assert.deepEqual([...decoded.payload], [255, 216, 255, 217]);
  assert.equal(decoded.payload.buffer, buffer);
  new Uint8Array(buffer)[64] = 42;
  assert.equal(decoded.payload[0], 42);
  const unavailable = decodeMediaMessage(media({flags: 0, timestamp: 0n}));
  assert.equal(unavailable.timestampNS, 0n);
  assert.equal(decodeMediaMessage(media({flags: 5})).timestampFlags, 5);
});

test("reserved audio messages retain exact timing and byte payload without PCM assumptions", () => {
  const decoded = decodeMediaMessage(media({kind: 2, flags: 5, width: 0, height: 0,
    crop: [0, 0, 0, 0], sampleFrames: 480, durationNS: 10000000, payload: [0, 128, 255, 127]}));
  assert.equal(decoded.kind, 2);
  assert.equal(decoded.sampleFrames, 480);
  assert.equal(decoded.durationNS, 10000000);
  assert.deepEqual([...decoded.payload], [0, 128, 255, 127]);
});

test("wire parser rejects malformed fixed headers, flags, geometry and bounds", () => {
  const unsupported = [media({kind: 3}), media(), media()];
  new DataView(unsupported[1]).setUint8(1, 2);
  new DataView(unsupported[2]).setUint16(2, 65, true);
  for (const buffer of [null, new Uint8Array(64), new ArrayBuffer(63), ...unsupported,
    media({flags: 8}), media({flags: 4, timestamp: 0n}), media({flags: 0}),
    media({sequence: 0n}), media({width: 0}), media({height: 0}),
    media({crop: [0, 0, 0, 1]}), media({crop: [0, 0, 1, 0]}),
    media({crop: [1920, 0, 1, 1]}), media({crop: [0, 1080, 1, 1]}),
    media({crop: [0xffffffff, 0, 1, 1]}), media({crop: [0, 0, 1921, 1]}),
    media({crop: [0, 0, 1, 1081]}), media({payload: []}),
    media({sampleFrames: 1}), media({durationNS: 1}),
    media({kind: 2, width: 0, height: 0, crop: [0, 0, 0, 0]}), // SOE is video-only.
    media({kind: 2, flags: 1}),
    media({kind: 2, flags: 1, width: 0, height: 0, crop: [1, 0, 0, 0]}),
  ]) assert.throws(() => decodeMediaMessage(buffer), RangeError);
});

test("new generation clears visible crop immediately and rejects a late old decode", () => {
  let resets = 0;
  const guard = createFrameGuard({onReset: () => { resets++; }});
  assert.equal(guard.generation, null);
  assert.equal(guard.sequence, null);
  assert.equal(guard.visible, null);
  const first = guard.begin(video());
  assert.equal(guard.commit(first), true);
  const delayed = guard.begin(video({sequence: 2n}));
  assert.equal(guard.visible, first);
  const next = guard.begin(video({generation: 2n, sequence: 3n,
    width: 1280, height: 720, crop: {x: 0, y: 0, width: 1280, height: 720}}));
  assert.equal(guard.visible, null);
  assert.equal(guard.generation, 2n);
  assert.equal(guard.sequence, 3n);
  assert.equal(guard.commit(delayed), false);
  assert.equal(guard.commit(next), true);
  assert.equal(guard.visible.crop.height, 720);
  assert.equal(resets, 2);
});

test("only the latest same-generation decode commits, with immutable small metadata", () => {
  const guard = createFrameGuard();
  const metadata = video({generation: 9007199254740993n, payload: new Uint8Array(100000)});
  const first = guard.begin(metadata);
  metadata.crop.y = 0;
  assert.equal(first.crop.y, 140);
  assert.equal("payload" in first, false);
  assert.equal(Object.isFrozen(first), true);
  assert.equal(Object.isFrozen(first.crop), true);
  const next = guard.begin(video({generation: 9007199254740993n, sequence: 2n}));
  assert.equal(guard.commit({...next}), false);
  assert.equal(guard.commit(first), false);
  assert.equal(guard.commit(null), false);
  assert.equal(guard.commit(next), true);
  assert.equal(guard.commit(next), false);
});

test("reconnection cannot commit a previous connection token with reused IDs", () => {
  const guard = createFrameGuard();
  const old = guard.begin(video());
  guard.reset();
  assert.equal(guard.generation, null);
  assert.equal(guard.sequence, null);
  const current = guard.begin(video());
  assert.equal(guard.commit(old), false);
  assert.equal(guard.commit(current), true);
});

test("frame admission rejects stale generations/sequences and malformed IDs/geometry", () => {
  const guard = createFrameGuard();
  const current = guard.begin(video({generation: 2n, sequence: 10n}));
  for (const metadata of [null, video({generation: 1n, sequence: 11n}),
    video({generation: 2n, sequence: 10n}), video({generation: 3n, sequence: 9n}),
    video({generation: 2, sequence: 11n}), video({generation: -1n}),
    video({generation: 1n << 64n}), video({sequence: 0n}), video({sequence: 2}),
    video({width: 0}), video({height: -1}), video({crop: null}),
    video({crop: {x: -1, y: 0, width: 1, height: 1}}),
    video({crop: {x: 0, y: 0.5, width: 1, height: 1}}),
    video({crop: {x: 0, y: 0, width: 2000, height: 1}}),
  ]) assert.equal(guard.begin(metadata), null);
  assert.equal(guard.commit(current), true);
});

test("controller starts disconnected and rejects ordinary input until connected", () => {
  const messages = [];
  const controller = createInputController({send: message => messages.push(message)});
  assert.equal(controller.enabled, false);
  assert.equal(controller.keyDown("KeyA"), false);
  assert.equal(controller.keyUp("KeyA"), false);
  assert.equal(controller.move(1, 2), false);
  assert.equal(controller.button("mousedown", 1, 2, 1), false);
  assert.equal(controller.scroll(1), false);
  assert.equal(controller.shortcut(["AltLeft", "Tab"]), false);
  assert.equal(controller.release(), false);
  assert.equal(controller.flush(), false);
  assert.deepEqual(messages, []);
  controller.setConnected(true);
  assert.equal(controller.enabled, true);
});

test("physical keys suppress repeat and unmatched up; shortcuts remain one complete action", () => {
  const {controller, messages} = input();
  assert.equal(controller.keyDown("KeyQ"), true);
  assert.equal(controller.keyDown("KeyQ"), false);
  const snapshot = controller.held;
  snapshot.keys.length = 0;
  assert.deepEqual(controller.held, {keys: ["KeyQ"], buttons: 0});
  const keys = ["ControlLeft", "AltLeft", "Delete"];
  assert.equal(controller.shortcut(keys), true);
  keys[0] = "KeyX";
  assert.deepEqual(controller.held, {keys: ["KeyQ"], buttons: 0});
  assert.equal(controller.keyUp("KeyQ"), true);
  assert.equal(controller.keyUp("KeyQ"), false);
  assert.deepEqual(messages, [
    {type: "keydown", code: "KeyQ"},
    {type: "shortcut", keys: ["ControlLeft", "AltLeft", "Delete"]},
    {type: "keyup", code: "KeyQ"},
  ]);
});

test("view-only transition releases held input before closing gate and ignores later up", () => {
  let controller;
  const messages = [];
  const releaseGateStates = [];
  controller = createInputController({send(message) {
    messages.push(message);
    if (message.type === "release_all") releaseGateStates.push([controller.enabled, controller.viewOnly]);
  }});
  controller.setConnected(true);
  controller.keyDown("KeyA");
  controller.button("mousedown", 10, 20, 1);
  controller.move(30, 40, 1);
  controller.setViewOnly(true);
  assert.deepEqual(releaseGateStates, [[true, false]]);
  assert.equal(controller.viewOnly, true);
  assert.equal(controller.enabled, false);
  assert.deepEqual(controller.held, {keys: [], buttons: 0});
  for (const send of [() => controller.keyDown("KeyB"), () => controller.keyUp("KeyA"),
    () => controller.move(1, 1), () => controller.button("mouseup", 1, 1, 0),
    () => controller.scroll(1), () => controller.shortcut(["AltLeft", "Tab"])]) assert.equal(send(), false);
  controller.setViewOnly(false);
  assert.equal(controller.keyUp("KeyA"), false);
  assert.equal(controller.button("mouseup", 1, 1, 0), false);
  assert.deepEqual(messages.map(message => message.type), ["keydown", "mousedown", "mousemove", "release_all"]);
});

test("focus and visibility transitions release only owned input and remain gated until restored", () => {
  for (const method of ["setFocused", "setVisible"]) {
    const {controller, time, messages} = input();
    controller.keyDown("AltLeft");
    controller[method](false);
    controller[method](false);
    assert.equal(controller.enabled, false);
    assert.equal(controller.keyDown("Tab"), false);
    assert.equal(time.pending, 0);
    controller[method](true);
    assert.equal(controller.enabled, true);
    assert.deepEqual(messages.map(message => message.type), ["keydown", "release_all"]);
    assert.deepEqual(controller.held, {keys: [], buttons: 0});
  }
});

test("passive blur/hide/view-only/disconnect discard movement without global release", () => {
  for (const [method, value] of [["setFocused", false], ["setVisible", false],
    ["setViewOnly", true], ["setConnected", false]]) {
    const {controller, time, messages} = input();
    controller.move(100, 200);
    assert.equal(time.pending, 1);
    controller[method](value);
    time.advance(1000);
    assert.equal(time.pending, 0);
    assert.deepEqual(messages, []);
    assert.deepEqual(controller.held, {keys: [], buttons: 0});
  }
});

test("explicit release clears held state while leaving the input gate open", () => {
  const {controller, time, messages} = input();
  controller.button("mousedown", 1, 2, 2);
  controller.move(4, 5, 2);
  assert.equal(controller.release(), true);
  assert.equal(controller.enabled, true);
  assert.deepEqual(controller.held, {keys: [], buttons: 0});
  assert.equal(time.pending, 0);
  assert.equal(controller.release(), false);
  assert.deepEqual(messages.map(message => message.type), ["mousedown", "mousemove", "release_all"]);
});

test("a later physical mouseup cannot restore buttons cleared at a gate transition", () => {
  const {controller, messages} = input();
  controller.button("mousedown", 1, 2, 1);
  controller.setViewOnly(true);
  controller.setViewOnly(false);
  assert.equal(controller.button("mouseup", 1, 2, 2), false);
  controller.button("mousedown", 1, 2, 2);
  // A previously-held physical left button remains down while right is released.
  assert.equal(controller.button("mouseup", 1, 2, 1), true);
  assert.deepEqual(messages.at(-1), {type: "mouseup", x: 1, y: 2, buttons: 0});
  assert.equal(controller.held.buttons, 0);
});

test("movement has one pending timer and only its latest position is sent", () => {
  const {controller, time, messages} = input();
  for (let i = 0; i < 4000; i++) controller.move(i, i, 0);
  assert.equal(time.pending, 1);
  assert.deepEqual(messages, []);
  time.advance(0);
  assert.deepEqual(messages, [{type: "mousemove", x: 3999, y: 3999, buttons: 0}]);
  assert.equal(time.pending, 0);
});

test("sustained movement is paced to the measured 30 Hz default", () => {
  const {controller, time, messages} = input();
  controller.move(0, 0);
  time.advance(0);
  controller.move(1, 1);
  time.advance(33);
  assert.equal(messages.length, 1);
  time.advance(1);
  assert.equal(messages.length, 2);
  controller.move(2, 2);
  time.advance(33);
  assert.equal(messages.length, 2);
  time.advance(1);
  assert.equal(messages.length, 3);
});

test("animation ticks flush only due movement and cancel its timeout", () => {
  const {controller, time, messages} = input({intervalMs: 20});
  assert.equal(controller.tick(), false);
  controller.move(0, 0);
  assert.equal(controller.tick(), true);
  assert.equal(time.pending, 0);
  controller.move(1, 1);
  time.jump(19);
  assert.equal(controller.tick(), false);
  time.jump(1);
  assert.equal(controller.tick(), true);
  assert.equal(time.pending, 0);
  time.advance(100);
  assert.equal(messages.length, 2);
});

test("key/button/scroll/shortcut events always flush earlier movement in order", () => {
  const {controller, time, messages} = input({intervalMs: 1000});
  controller.move(10, 20);
  controller.keyDown("KeyA");
  controller.move(30, 40);
  controller.button("mousedown", 30, 40, 1);
  controller.move(50, 60, 1);
  controller.keyUp("KeyA");
  controller.move(70, 80, 1);
  controller.button("mouseup", 70, 80, 0);
  controller.move(90, 100);
  controller.scroll(-4.5);
  controller.move(110, 120);
  controller.shortcut(["AltLeft", "Tab"]);
  time.advance(1000);
  assert.equal(time.pending, 0);
  assert.deepEqual(messages.map(message => message.type), [
    "mousemove", "keydown", "mousemove", "mousedown", "mousemove", "keyup",
    "mousemove", "mouseup", "mousemove", "scroll", "mousemove", "shortcut",
  ]);
  assert.deepEqual(messages.filter(message => message.type === "mousemove").map(message => message.x),
    [10, 30, 50, 70, 90, 110]);
});

test("disconnect with held input never sends onto the closed wire and resets reconnect ownership", () => {
  const {controller, time, messages} = input();
  controller.keyDown("KeyA");
  controller.button("mousedown", 10, 20, 1);
  controller.move(30, 40);
  controller.setConnected(false);
  time.advance(1000);
  assert.deepEqual(messages.map(message => message.type), ["keydown", "mousedown"]);
  assert.equal(controller.enabled, false);
  assert.deepEqual(controller.held, {keys: [], buttons: 0});
  controller.setConnected(true);
  assert.equal(controller.keyUp("KeyA"), false);
  assert.equal(controller.button("mouseup", 1, 2, 0), false);
  controller.move(100, 100);
  time.advance(0);
  assert.equal(messages.at(-1).type, "mousemove");
});

test("unavailable or throwing sends discard held input and pending movement without replay", () => {
  for (const throws of [false, true]) {
    let fail = false;
    const messages = [];
    const {controller, time} = input({send(message) {
      if (fail) {
        if (throws) throw new Error("socket closed");
        return false;
      }
      messages.push(message);
    }});
    controller.keyDown("KeyA");
    controller.move(10, 20);
    fail = true;
    assert.equal(controller.button("mousedown", 30, 40, 1), false);
    assert.equal(controller.enabled, false);
    assert.deepEqual(controller.held, {keys: [], buttons: 0});
    assert.equal(time.pending, 0);
    fail = false;
    controller.setConnected(true);
    assert.equal(controller.keyUp("KeyA"), false);
    controller.keyDown("KeyB");
    assert.deepEqual(messages.map(message => message.code), ["KeyA", "KeyB"]);
  }
});

test("failed keyup or release forgets local ownership when the wire has gone away", () => {
  for (const action of [controller => controller.keyUp("KeyA"), controller => controller.release()]) {
    let accepted = true;
    const {controller} = input({send: () => accepted});
    controller.keyDown("KeyA");
    accepted = false;
    assert.equal(action(controller), false);
    assert.equal(controller.enabled, false);
    assert.deepEqual(controller.held, {keys: [], buttons: 0});
  }
});

test("malformed local events are ignored before input or pending movement is sent", () => {
  const {controller, time, messages} = input();
  const invalid = [() => controller.keyDown(""), () => controller.keyDown(null),
    () => controller.shortcut([]), () => controller.shortcut(null),
    () => controller.shortcut([""]), () => controller.shortcut([1]),
    () => controller.scroll(NaN), () => controller.scroll(Infinity),
    () => controller.button("click", 0, 0, 0), () => controller.button("mousedown", 0, 0, 8),
    () => controller.move(-1, 0), () => controller.move(4096, 0),
    () => controller.move(0, -1), () => controller.move(0, 4096),
    () => controller.move(0.5, 0), () => controller.move(0, NaN),
    () => controller.move(0, 0, -1), () => controller.move(0, 0, 1.5),
  ];
  for (const action of invalid) assert.equal(action(), false);
  assert.equal(time.pending, 0);
  assert.deepEqual(messages, []);
  for (const options of [{}, {send: true}, {send() {}, intervalMs: 0},
    {send() {}, intervalMs: Infinity}]) assert.throws(() => createInputController(options), TypeError);
});


test("token fragment becomes an encoded session cookie and is removed from the URL", async () => {
  const {tokenBootstrap} = await import("./assets/viewer-helpers.mjs");
  assert.equal(tokenBootstrap("http://localhost:9330/"), null);
  assert.equal(tokenBootstrap("http://localhost:9330/#screen"), null);
  assert.deepEqual(tokenBootstrap("http://localhost:9330/?view=1#token=a%2B%3B%20%C3%A5%25"), {
    cookie: "autopaws_token=a%2B%3B%20%C3%A5%25; Path=/; SameSite=Strict",
    url: "http://localhost:9330/?view=1",
  });
  assert.deepEqual(tokenBootstrap("https://localhost:9330/#token=next"), {
    cookie: "autopaws_token=next; Path=/; SameSite=Strict; Secure", url: "https://localhost:9330/",
  });
  assert.deepEqual(tokenBootstrap("http://localhost:9330/#token="), {
    cookie: "autopaws_token=; Path=/; SameSite=Strict", url: "http://localhost:9330/",
  });
});
