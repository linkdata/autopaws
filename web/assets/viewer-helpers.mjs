const MAX_U64 = (1n << 64n) - 1n;
const HEADER_SIZE = 64;
const DEFAULT_MOVE_INTERVAL_MS = 1000 / 30;

function positiveInteger(value) {
  return Number.isSafeInteger(value) && value > 0;
}

function validCrop(width, height, crop) {
  return positiveInteger(width) && positiveInteger(height) && crop !== null &&
    typeof crop === "object" && Number.isSafeInteger(crop.x) && crop.x >= 0 &&
    Number.isSafeInteger(crop.y) && crop.y >= 0 && positiveInteger(crop.width) &&
    positiveInteger(crop.height) && crop.width <= width - crop.x &&
    crop.height <= height - crop.y;
}

// Map positions on the displayed, already-cropped canvas to HID coordinates.
// Its first and last displayed CSS pixel positions map exactly to 0 and 4095.
// Return null while layout has no usable canvas dimensions.
export function mapPointer(clientX, clientY, rect, visibleWidth, visibleHeight) {
  if (![clientX, clientY, rect?.left, rect?.top, rect?.width, rect?.height]
    .every(Number.isFinite) || rect.width <= 0 || rect.height <= 0 ||
    !positiveInteger(visibleWidth) || !positiveInteger(visibleHeight)) return null;
  const axis = (position, start, size, pixels) => {
    if (pixels === 1 || size <= 1) return 0;
    const fraction = Math.min(1, Math.max(0, (position - start) / (size - 1)));
    return Math.round(fraction * 4095);
  };
  return {
    x: axis(clientX, rect.left, rect.width, visibleWidth),
    y: axis(clientY, rect.top, rect.height, visibleHeight),
  };
}

// Decode the fixed v1 header without losing uint64 precision. Payload is a view
// of the supplied buffer; video bytes remain the original JPEG. Audio fields are
// reserved until the audio viewer is implemented and are not interpreted here.
export function decodeMediaMessage(buffer) {
  if (!(buffer instanceof ArrayBuffer) || buffer.byteLength < HEADER_SIZE) {
    throw new RangeError("media message is shorter than its header");
  }
  const view = new DataView(buffer);
  const kind = view.getUint8(0);
  if ((kind !== 1 && kind !== 2) || view.getUint8(1) !== 1 ||
    view.getUint16(2, true) !== HEADER_SIZE) throw new RangeError("unsupported media header");
  const timestampFlags = view.getUint32(4, true);
  const timestampNS = view.getBigUint64(24, true);
  if ((timestampFlags & ~7) !== 0 || ((timestampFlags & 4) !== 0 && (timestampFlags & 1) === 0) ||
    ((timestampFlags & 1) === 0 && timestampNS !== 0n)) throw new RangeError("invalid media timestamp");
  const metadata = {
    kind,
    generation: view.getBigUint64(8, true),
    sequence: view.getBigUint64(16, true),
    timestampNS,
    timestampFlags,
    width: view.getUint32(32, true),
    height: view.getUint32(36, true),
    crop: {
      x: view.getUint32(40, true), y: view.getUint32(44, true),
      width: view.getUint32(48, true), height: view.getUint32(52, true),
    },
    sampleFrames: view.getUint32(56, true),
    durationNS: view.getUint32(60, true),
    payload: new Uint8Array(buffer, HEADER_SIZE),
  };
  if (kind === 1) {
    if (metadata.sequence === 0n || !validCrop(metadata.width, metadata.height, metadata.crop) ||
      metadata.payload.byteLength === 0 || metadata.sampleFrames !== 0 || metadata.durationNS !== 0) {
      throw new RangeError("invalid video metadata");
    }
  } else if (metadata.width !== 0 || metadata.height !== 0 ||
    Object.values(metadata.crop).some(value => value !== 0) || (timestampFlags & 2) !== 0) {
    throw new RangeError("invalid audio metadata");
  }
  return metadata;
}

function validID(value) {
  return typeof value === "bigint" && value >= 0n && value <= MAX_U64;
}

// begin() is called when a decode is selected, and immediately on arrival of a
// new generation. Keep that token with the selected JPEG. commit() must succeed
// before drawing it. At most one current token exists; no decode queue is kept.
export function createFrameGuard({onReset = () => {}} = {}) {
  let generation = null;
  let sequence = null;
  let pending = null;
  let visible = null;
  return {
    get generation() { return generation; },
    get sequence() { return sequence; },
    get visible() { return visible; },
    begin(metadata) {
      if (!validID(metadata?.generation) || !validID(metadata?.sequence) || metadata.sequence === 0n ||
        !validCrop(metadata.width, metadata.height, metadata.crop)) return null;
      if ((generation !== null && metadata.generation < generation) ||
        (sequence !== null && metadata.sequence <= sequence)) return null;
      const changed = metadata.generation !== generation;
      generation = metadata.generation;
      sequence = metadata.sequence;
      pending = Object.freeze({
        kind: metadata.kind, generation, sequence,
        timestampNS: metadata.timestampNS, timestampFlags: metadata.timestampFlags,
        width: metadata.width, height: metadata.height,
        crop: Object.freeze({...metadata.crop}),
      });
      if (changed) {
        visible = null;
        onReset();
      }
      return pending;
    },
    commit(token) {
      if (token === null || token !== pending) return false;
      visible = token;
      pending = null;
      return true;
    },
    reset() {
      generation = null;
      sequence = null;
      pending = null;
      visible = null;
      onReset();
    },
  };
}

function validPosition(x, y, buttons) {
  return Number.isInteger(x) && x >= 0 && x <= 4095 && Number.isInteger(y) && y >= 0 &&
    y <= 4095 && Number.isInteger(buttons) && buttons >= 0 && buttons <= 7;
}

// send(message) accepts synchronously; returning false or throwing means the
// connection is unavailable. There is only one pending move and one timer.
// Discrete input flushes that move first, even before the 30 Hz interval expires.
export function createInputController({send, schedule = setTimeout, cancel = clearTimeout,
  now = () => performance.now(), intervalMs = DEFAULT_MOVE_INTERVAL_MS}) {
  if (typeof send !== "function" || !Number.isFinite(intervalMs) || intervalMs <= 0) {
    throw new TypeError("input controller requires send and a positive movement interval");
  }
  let connected = false;
  let viewOnly = false;
  let focused = true;
  let visible = true;
  const keys = new Set();
  let buttons = 0;
  let pending = null;
  let timer = null;
  let lastMoveAt = -Infinity;
  const enabled = () => connected && !viewOnly && focused && visible;
  const clearPending = () => {
    if (timer !== null) cancel(timer);
    timer = null;
    pending = null;
  };
  const disconnected = () => {
    connected = false;
    clearPending();
    keys.clear();
    buttons = 0;
    lastMoveAt = -Infinity;
  };
  const emit = message => {
    if (!enabled()) return false;
    try {
      if (send(message) !== false) return true;
    } catch {
      // A failed/dead WebSocket cannot carry a release; the server owns its
      // disconnect cleanup. Forget local ownership before a later reconnect.
    }
    disconnected();
    return false;
  };
  const flush = () => {
    const move = pending;
    clearPending();
    if (move === null || !emit(move)) return false;
    lastMoveAt = now();
    return true;
  };
  const release = () => {
    const owned = keys.size > 0 || buttons !== 0;
    if (owned) flush();
    else clearPending();
    const sent = owned && emit({type: "release_all"});
    keys.clear();
    buttons = 0;
    return sent;
  };
  const discrete = message => {
    if (!enabled()) return false;
    flush();
    return emit(message);
  };
  const setGate = (value, previous, update) => {
    value = Boolean(value);
    if (value === previous) return;
    if (enabled()) release();
    update(value);
  };
  return {
    get enabled() { return enabled(); },
    get viewOnly() { return viewOnly; },
    get held() { return {keys: [...keys], buttons}; },
    setConnected(value) {
      if (!value) disconnected();
      else connected = true;
    },
    setViewOnly(value) { setGate(value, viewOnly, next => { viewOnly = next; }); },
    setFocused(value) { setGate(value, focused, next => { focused = next; }); },
    setVisible(value) { setGate(value, visible, next => { visible = next; }); },
    release,
    flush,
    // Optional animation-loop tick. The single timer also flushes a due move
    // when requestAnimationFrame is unavailable or throttled.
    tick() {
      if (pending !== null && now() - lastMoveAt >= intervalMs) return flush();
      return false;
    },
    keyDown(code) {
      if (typeof code !== "string" || code.length === 0 || keys.has(code)) return false;
      if (!discrete({type: "keydown", code})) return false;
      keys.add(code);
      return true;
    },
    keyUp(code) {
      if (!keys.has(code)) return false;
      const sent = discrete({type: "keyup", code});
      keys.delete(code);
      return sent;
    },
    move(x, y, mask = buttons) {
      if (!enabled() || !validPosition(x, y, mask)) return false;
      pending = {type: "mousemove", x, y, buttons: mask};
      if (timer === null) {
        timer = schedule(() => { timer = null; flush(); }, Math.ceil(Math.max(0, lastMoveAt + intervalMs - now())));
      }
      return true;
    },
    button(type, x, y, mask) {
      if ((type !== "mousedown" && type !== "mouseup") || !validPosition(x, y, mask)) return false;
      // A later physical up for a button released at a gate transition cannot
      // reacquire another button that was held while input was disabled.
      if (type === "mouseup") {
        mask &= buttons;
        if (buttons === mask) return false;
      }
      if (!discrete({type, x, y, buttons: mask})) return false;
      buttons = mask;
      return true;
    },
    scroll(deltaY) {
      return Number.isFinite(deltaY) && discrete({type: "scroll", deltaY});
    },
    shortcut(shortcutKeys) {
      if (!Array.isArray(shortcutKeys) || shortcutKeys.length === 0 ||
        shortcutKeys.some(code => typeof code !== "string" || code.length === 0)) return false;
      return discrete({type: "shortcut", keys: [...shortcutKeys]});
    },
  };
}
