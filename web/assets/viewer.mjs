import {mapPointer, decodeMediaMessage, createFrameGuard, createInputController, tokenBootstrap}
  from "./viewer-helpers.mjs";
import {createAudioPlayback} from "./audio-playback.mjs";

const authentication = tokenBootstrap(location.href);
if (authentication !== null) {
  history.replaceState(null, "", authentication.url);
  document.cookie = authentication.cookie;
}

const byID = id => document.getElementById(id);
const toolbar = byID("toolbar");
const viewport = byID("viewport");
const canvas = byID("screen");
const context = canvas.getContext("2d", {alpha: false});
const message = byID("message");
const status = byID("status");
const shortcutButtons = [byID("ctrl-alt-del"), byID("alt-tab")];
let socket = null;
let reconnectTimer = null;
let reconnectDelay = 500;
let leaving = false;
let localCursor = true;
let nativeSize = false;
let decoding = false;
let pendingVideo = null;
let renderedFrames = 0;
let fpsSince = performance.now();
let hideTimer = null;
let dragging = null;
const capabilityMessages = new Map();
const audio = createAudioPlayback({
  AudioContext: window.AudioContext ?? window.webkitAudioContext,
  AudioWorkletNode: window.AudioWorkletNode,
  secureContext: window.isSecureContext,
  onState(state) {
    const button = byID("audio");
    button.hidden = state.status !== "available";
    button.setAttribute("aria-pressed", String(state.playing));
    button.textContent = state.enabled && !state.playing ? (state.retryable ? "Retry audio" : "Cancel audio") : "Audio";
    button.title = state.playing ? "Mute audio" : state.enabled ?
      (state.retryable ? "Enable audio again to resume" : "Cancel enabling audio") : "Enable audio";
    explainCapability("audio", state.explanation);
    if (state.explanation) byID("capabilities").open = true;
  },
});

function showMessage(text) {
  message.textContent = text;
  message.hidden = false;
}

const frames = createFrameGuard({onReset() {
  // Coordinates disappear during a stream change. Release ownership without
  // needing a replacement image or guessing coordinates for a later mouseup.
  input.release();
  canvas.width = 1;
  canvas.height = 1;
  context?.clearRect(0, 0, 1, 1);
  showMessage("Waiting for video…");
}});

function send(event) {
  if (socket?.readyState !== WebSocket.OPEN) return false;
  // A network stall must not turn into an unlimited browser input buffer.
  if (socket.bufferedAmount > 4096) {
    socket.close(1013, "Input connection is backlogged");
    return false;
  }
  socket.send(JSON.stringify(event));
  return true;
}

const input = createInputController({send});
input.setFocused(document.hasFocus());
input.setVisible(!document.hidden);

function updateControls() {
  byID("view-only").setAttribute("aria-pressed", String(input.viewOnly));
  byID("view-only").title = input.viewOnly ? "Enable keyboard and mouse input" : "Stop sending keyboard and mouse input";
  byID("cursor").setAttribute("aria-pressed", String(localCursor));
  byID("scaling").setAttribute("aria-pressed", String(nativeSize));
  byID("scaling").textContent = nativeSize ? "1:1" : "Fit";
  for (const button of shortcutButtons) button.disabled = !input.enabled;
  canvas.style.cursor = input.enabled ? (localCursor ? "crosshair" : "none") : "default";
}

function updateLayout() {
  const fullscreen = Boolean(document.fullscreenElement);
  viewport.style.top = fullscreen ? "0px" : `${toolbar.offsetHeight + 24}px`;
  viewport.classList.toggle("native-size", nativeSize);
  const visible = frames.visible;
  if (visible === null) return;
  const {width, height} = visible.crop;
  const scale = nativeSize ? 1 : Math.min(viewport.clientWidth / width, viewport.clientHeight / height);
  const displayWidth = Math.max(1, Math.floor(width * scale));
  const displayHeight = Math.max(1, Math.floor(height * scale));
  canvas.style.width = `${displayWidth}px`;
  canvas.style.height = `${displayHeight}px`;
  // Integer origins keep the first and last visible CSS pixels addressable
  // on a DPR1 display, including when centering leaves an odd spare pixel.
  canvas.style.left = `${Math.max(0, Math.floor((viewport.clientWidth - displayWidth) / 2))}px`;
  canvas.style.top = `${Math.max(0, Math.floor((viewport.clientHeight - displayHeight) / 2))}px`;
}

function explainCapability(name, text) {
  if (text) capabilityMessages.set(name, text);
  else capabilityMessages.delete(name);
  const list = byID("capability-list");
  list.replaceChildren(...[...capabilityMessages.values()].map(text => {
    const item = document.createElement("li");
    item.textContent = text;
    return item;
  }));
  byID("capabilities").hidden = capabilityMessages.size === 0;
}

function connectionStatus(text, className) {
  status.textContent = text;
  status.className = `status ${className}`;
}

async function decodeJPEG(payload) {
  const blob = new Blob([payload], {type: "image/jpeg"});
  if (typeof createImageBitmap === "function") return createImageBitmap(blob);
  return new Promise((resolve, reject) => {
    const image = new Image();
    const url = URL.createObjectURL(blob);
    image.onload = () => { URL.revokeObjectURL(url); resolve(image); };
    image.onerror = () => { URL.revokeObjectURL(url); reject(new Error("JPEG decode failed")); };
    image.src = url;
  });
}

async function decodePending() {
  if (decoding || pendingVideo === null || document.hidden || !context) return;
  const item = pendingVideo;
  pendingVideo = null;
  const token = item.token ?? frames.begin(item.frame);
  if (token === null) return;
  decoding = true;
  let image;
  try {
    image = await decodeJPEG(item.frame.payload);
    if (image.width !== item.frame.width || image.height !== item.frame.height) {
      throw new Error("JPEG dimensions differ from frame metadata");
    }
    if (document.hidden || !frames.commit(token)) return;
    const crop = item.frame.crop;
    if (canvas.width !== crop.width) canvas.width = crop.width;
    if (canvas.height !== crop.height) canvas.height = crop.height;
    context.drawImage(image, crop.x, crop.y, crop.width, crop.height,
      0, 0, crop.width, crop.height);
    message.hidden = true;
    renderedFrames++;
    updateLayout();
  } catch {
    if (frames.visible === null) showMessage("Waiting for a readable video frame…");
  } finally {
    image?.close?.();
    decoding = false;
    // Exactly one decode and one latest pending JPEG are retained. Completing
    // a same-generation decode can display while the next one waits, so a slow
    // decoder cannot be starved by a continuously arriving stream.
    void decodePending();
  }
}

function videoFrame(frame) {
  if (frames.generation !== null && (frame.generation < frames.generation ||
    frame.sequence <= frames.sequence)) return;
  let token = null;
  if (frames.generation === null || frame.generation > frames.generation) {
    token = frames.begin(frame); // Invalidate an old decode immediately.
    if (token === null) return;
  }
  if (pendingVideo !== null && pendingVideo.frame.generation === frame.generation &&
    pendingVideo.frame.sequence >= frame.sequence) return;
  pendingVideo = {frame, token};
  void decodePending();
}

function connect() {
  if (leaving) return;
  clearTimeout(reconnectTimer);
  reconnectTimer = null;
  input.setConnected(false);
  void audio.reset();
  pendingVideo = null;
  frames.reset();
  updateControls();
  connectionStatus("Connecting…", "");
  showMessage("Connecting to the KVM…");
  const url = new URL("/ws", location.href);
  url.protocol = location.protocol === "https:" ? "wss:" : "ws:";
  const connection = new WebSocket(url);
  connection.binaryType = "arraybuffer";
  socket = connection;
  connection.addEventListener("open", () => {
    if (socket !== connection) return;
    reconnectDelay = 500;
    input.setConnected(true);
    connectionStatus("Connected", "connected");
    showMessage("Waiting for video…");
    updateControls();
  });
  connection.addEventListener("message", async event => {
    if (socket !== connection) return;
    try {
      if (typeof event.data === "string") {
        const config = JSON.parse(event.data);
        if (config.type !== "config") throw new Error("Unexpected configuration");
        void audio.configure(config.audio);
        return;
      }
      const data = event.data instanceof ArrayBuffer ? event.data : await event.data.arrayBuffer();
      if (socket !== connection) return;
      const frame = decodeMediaMessage(data);
      if (frame.kind === 1) videoFrame(frame);
      else audio.push(frame);
    } catch {
      connection.close(1002, "Invalid stream data");
    }
  });
  connection.addEventListener("error", () => connection.close());
  connection.addEventListener("close", () => {
    if (socket !== connection) return;
    input.setConnected(false);
    void audio.reset();
    pendingVideo = null;
    frames.reset();
    renderedFrames = 0;
    byID("fps").textContent = "0 fps";
    connectionStatus("Reconnecting…", "disconnected");
    showMessage("Connection lost. Reconnecting…");
    updateControls();
    if (!leaving) {
      reconnectTimer = setTimeout(connect, reconnectDelay);
      reconnectDelay = Math.min(5000, reconnectDelay * 2);
    }
  });
}

function focusScreen() { viewport.focus({preventScroll: true}); }

function pointerPosition(event) {
  const visible = frames.visible;
  if (!visible) return null;
  return mapPointer(event.clientX, event.clientY, canvas.getBoundingClientRect(),
    visible.crop.width, visible.crop.height);
}

viewport.addEventListener("keydown", event => {
  if (!input.enabled) return;
  event.preventDefault();
  if (!event.repeat) input.keyDown(event.code);
});
viewport.addEventListener("keyup", event => {
  if (!input.enabled) return;
  event.preventDefault();
  input.keyUp(event.code);
});
viewport.addEventListener("blur", () => input.release());

canvas.addEventListener("pointerdown", event => {
  focusScreen();
  if (!input.enabled) return;
  try { canvas.setPointerCapture(event.pointerId); } catch { /* Optional for synthetic events. */ }
});
// Mouse events include every button transition. Pointer down/up only describe
// zero-to-nonzero/nonzero-to-zero and would lose multi-button chords.
window.addEventListener("mousedown", event => {
  if (!input.enabled || (event.target !== canvas && input.held.buttons === 0)) return;
  event.preventDefault();
  const point = pointerPosition(event);
  if (point) input.button("mousedown", point.x, point.y, event.buttons & 7);
});
window.addEventListener("mousemove", event => {
  if (!input.enabled || (event.target !== canvas && input.held.buttons === 0)) return;
  const point = pointerPosition(event);
  if (point) input.move(point.x, point.y, event.buttons & 7);
});
window.addEventListener("mouseup", event => {
  if (!input.enabled || input.held.buttons === 0) return;
  event.preventDefault();
  const point = pointerPosition(event);
  if (point) input.button("mouseup", point.x, point.y, event.buttons & 7);
  else input.release(); // Layout may be temporarily empty even on the same stream.
});
canvas.addEventListener("pointercancel", () => input.release());
canvas.addEventListener("wheel", event => {
  if (!input.enabled || event.deltaY === 0) return;
  event.preventDefault();
  input.scroll(event.deltaY > 0 ? -3 : 3);
}, {passive: false});
canvas.addEventListener("contextmenu", event => { if (input.enabled) event.preventDefault(); });

byID("ctrl-alt-del").addEventListener("click", () => {
  input.shortcut(["ControlLeft", "AltLeft", "Delete"]);
  focusScreen();
});
byID("alt-tab").addEventListener("click", () => {
  input.shortcut(["AltLeft", "Tab"]);
  focusScreen();
});
byID("view-only").addEventListener("click", async () => {
  input.setViewOnly(!input.viewOnly); // The helper releases before closing its gate.
  updateControls();
  await keyboardLock();
  focusScreen();
});
byID("audio").addEventListener("click", () => {
  // A loading graph can be cancelled; a suspended/failed graph needs a fresh
  // enable gesture. The visible label distinguishes those two actions.
  const state = audio.state;
  void audio.setEnabled(!state.enabled || state.retryable);
  focusScreen();
});
byID("cursor").addEventListener("click", () => {
  localCursor = !localCursor;
  updateControls();
  focusScreen();
});
byID("scaling").addEventListener("click", () => {
  nativeSize = !nativeSize;
  updateControls();
  updateLayout();
  focusScreen();
});

async function keyboardLock() {
  if (!document.fullscreenElement || input.viewOnly) {
    navigator.keyboard?.unlock?.();
    return;
  }
  if (typeof navigator.keyboard?.lock !== "function") return;
  try {
    await navigator.keyboard.lock();
    explainCapability("keyboard", "");
  } catch {
    explainCapability("keyboard", "This browser did not allow Keyboard Lock. Use the toolbar to send shortcuts that stay local.");
  }
}

function showToolbar() {
  toolbar.classList.remove("is-hidden");
  clearTimeout(hideTimer);
  hideTimer = null;
  if (!document.fullscreenElement) return;
  hideTimer = setTimeout(() => {
    hideTimer = null;
    if (dragging || toolbar.matches(":hover") || toolbar.contains(document.activeElement)) {
      showToolbar();
    } else toolbar.classList.add("is-hidden");
  }, 1800);
}

byID("fullscreen").addEventListener("click", async () => {
  try {
    if (document.fullscreenElement) await document.exitFullscreen();
    else await document.documentElement.requestFullscreen();
    await keyboardLock();
  } catch {
    explainCapability("fullscreen", "Fullscreen is unavailable in this browser or was not allowed.");
  }
  focusScreen();
});
document.addEventListener("fullscreenchange", () => {
  const active = Boolean(document.fullscreenElement);
  document.body.classList.toggle("fullscreen", active);
  byID("fullscreen").setAttribute("aria-pressed", String(active));
  byID("fullscreen").textContent = active ? "Exit fullscreen" : "Fullscreen";
  byID("toolbar-handle").hidden = !active;
  if (!active) {
    toolbar.style.left = "";
    toolbar.style.top = "";
    navigator.keyboard?.unlock?.();
  }
  showToolbar();
  updateLayout();
});

function moveToolbar(left, top) {
  toolbar.style.left = `${Math.round(Math.max(0, Math.min(left, innerWidth - toolbar.offsetWidth)))}px`;
  toolbar.style.top = `${Math.round(Math.max(0, Math.min(top, innerHeight - toolbar.offsetHeight)))}px`;
}

byID("toolbar-handle").addEventListener("pointerdown", event => {
  if (!document.fullscreenElement) return;
  event.preventDefault();
  const rect = toolbar.getBoundingClientRect();
  dragging = {x: event.clientX - rect.left, y: event.clientY - rect.top};
  event.currentTarget.setPointerCapture(event.pointerId);
  showToolbar();
});
byID("toolbar-handle").addEventListener("pointermove", event => {
  if (dragging) moveToolbar(event.clientX - dragging.x, event.clientY - dragging.y);
});
const finishDrag = () => { dragging = null; showToolbar(); focusScreen(); };
byID("toolbar-handle").addEventListener("pointerup", finishDrag);
byID("toolbar-handle").addEventListener("pointercancel", finishDrag);
byID("toolbar-handle").addEventListener("keydown", event => {
  const delta = {ArrowLeft: [-8, 0], ArrowRight: [8, 0], ArrowUp: [0, -8], ArrowDown: [0, 8]}[event.code];
  if (!delta || !document.fullscreenElement) return;
  event.preventDefault();
  const rect = toolbar.getBoundingClientRect();
  moveToolbar(rect.left + delta[0], rect.top + delta[1]);
  showToolbar();
});
window.addEventListener("pointermove", event => {
  if (document.fullscreenElement && (event.clientY < 32 || toolbar.contains(event.target))) showToolbar();
});
window.addEventListener("blur", () => { input.setFocused(false); updateControls(); });
window.addEventListener("focus", () => { input.setFocused(true); updateControls(); });
document.addEventListener("visibilitychange", () => {
  input.setVisible(!document.hidden);
  updateControls();
  if (!document.hidden) void decodePending();
});
window.addEventListener("resize", () => {
  if (document.fullscreenElement) {
    const rect = toolbar.getBoundingClientRect();
    moveToolbar(rect.left, rect.top);
  }
  updateLayout();
});
if (typeof ResizeObserver === "function") new ResizeObserver(updateLayout).observe(toolbar);

window.addEventListener("pagehide", () => {
  leaving = true;
  clearTimeout(reconnectTimer);
  input.release();
  input.setConnected(false);
  void audio.reset();
  socket?.close(1000, "Viewer closing");
});
window.addEventListener("pageshow", event => {
  if (event.persisted) { leaving = false; connect(); }
});

setInterval(() => {
  const now = performance.now();
  byID("fps").textContent = `${Math.round(renderedFrames * 1000 / (now - fpsSince))} fps`;
  renderedFrames = 0;
  fpsSince = now;
}, 1000);

byID("toolbar-handle").hidden = true;
if (!context) explainCapability("canvas", "This browser cannot render the video canvas.");
if (typeof navigator.keyboard?.lock !== "function") {
  explainCapability("keyboard", "Keyboard Lock is unavailable. Toolbar shortcuts still work; fullscreen may leave some browser shortcuts local.");
}
if (!document.fullscreenEnabled || typeof document.documentElement.requestFullscreen !== "function") {
  byID("fullscreen").disabled = true;
  explainCapability("fullscreen", "Fullscreen is unavailable in this browser.");
}
if ("serviceWorker" in navigator && window.isSecureContext) {
  navigator.serviceWorker.register("/sw.js").catch(() => {
    explainCapability("install", "Installable app support could not be enabled. The viewer remains available in this tab.");
  });
} else {
  explainCapability("install", "Installable app support needs a compatible browser and a secure context. A localhost SSH tunnel is sufficient.");
}
updateControls();
updateLayout();
connect();
