import {trimAudioBlock} from "./viewer-helpers.mjs";

function parseConfig(value) {
  if (!value || !["off", "unavailable", "available"].includes(value.status) ||
    typeof value.generation !== "string" || !/^(0|[1-9][0-9]*)$/.test(value.generation)) return null;
  const generation = BigInt(value.generation);
  if (generation > (1n << 64n) - 1n) return null;
  if (value.status === "available" && (!Number.isSafeInteger(value.sampleRate) || value.sampleRate <= 0 ||
    (value.channels !== 1 && value.channels !== 2))) return null;
  return {status: value.status, generation, sampleRate: value.sampleRate, channels: value.channels};
}

// The page supplies capabilities explicitly so missing audio never affects
// video/input. setEnabled(true) should be called directly from a user gesture;
// it also retries a failed/suspended setup without losing the user's choice.
export function createAudioPlayback({AudioContext, AudioWorkletNode, secureContext,
  workletURL = new URL("./audio-worklet.mjs", import.meta.url), onState = () => {}}) {
  let config = {status: "unavailable", generation: 0n};
  let enabled = false;
  let playing = false;
  let explanation = "";
  let epoch = 0;
  let context = null;
  let node = null;
  let setup = null;
  let inFlight = null;
  const state = () => ({status: config.status, enabled, playing,
    retryable: enabled && !playing && (context === null || context.state === "suspended"), explanation});
  const changed = () => onState(state());
  const closeContext = candidate => {
    if (candidate.state === "closed") return Promise.resolve();
    try { return Promise.resolve(candidate.close()).catch(() => {}); }
    catch { return Promise.resolve(); }
  };
  const teardown = () => {
    epoch++;
    playing = false;
    inFlight = null;
    setup = null;
    if (node !== null) {
      node.disconnect();
      node.port.close();
      node = null;
    }
    const previous = context;
    context = null;
    return previous === null ? Promise.resolve() : closeContext(previous);
  };
  const fail = (token, text) => {
    if (token !== epoch) return;
    explanation = text;
    void teardown();
    changed();
  };
  const start = () => {
    if (!enabled || config.status !== "available") return Promise.resolve(false);
    if (playing) return Promise.resolve(true);
    if (setup !== null) return setup;
    explanation = "";
    if (!secureContext || typeof AudioContext !== "function" || typeof AudioWorkletNode !== "function") {
      explanation = "AudioWorklet needs a compatible browser and secure context. A localhost SSH tunnel is sufficient.";
      changed();
      return Promise.resolve(false);
    }
    const token = epoch;
    let candidate;
    try {
      candidate = new AudioContext({sampleRate: config.sampleRate});
      context = candidate;
      if (candidate.sampleRate !== config.sampleRate) {
        fail(token, `This browser cannot play the negotiated ${config.sampleRate} Hz rate. No resampler is enabled.`);
        return Promise.resolve(false);
      }
      if (typeof candidate.audioWorklet?.addModule !== "function") {
        fail(token, "AudioWorklet is unavailable in this browser.");
        return Promise.resolve(false);
      }
      // Invoke resume during the gesture, before awaiting module loading.
      const selected = config;
      candidate.addEventListener("statechange", () => {
        if (token !== epoch) return;
        if (playing && candidate.state !== "running") {
          fail(token, "Audio playback was interrupted. Enable audio again to resume.");
        } else changed();
      });
      const resumed = Promise.resolve(candidate.resume());
      const loaded = Promise.resolve().then(() => candidate.audioWorklet.addModule(workletURL));
      setup = (async () => {
        try {
          await Promise.all([resumed, loaded]);
          if (token !== epoch) return false;
          if (candidate.state !== "running") throw new Error("Audio context is not running");
          const created = new AudioWorkletNode(candidate, "autopaws-audio", {
            numberOfInputs: 0, numberOfOutputs: 1, outputChannelCount: [selected.channels],
            processorOptions: {sampleRate: selected.sampleRate, channels: selected.channels,
              generation: selected.generation},
          });
          node = created;
          created.port.onmessage = event => {
            if (token !== epoch || node !== created) return;
            const ack = event.data;
            if (ack?.type === "ack" && inFlight !== null && ack.generation === inFlight.generation &&
              ack.sequence === inFlight.sequence) inFlight = null;
          };
          created.addEventListener("processorerror", () => {
            fail(token, "The audio worklet stopped. Enable audio again to retry.");
          });
          created.connect(candidate.destination);
          playing = true;
          explanation = "";
          changed();
          return true;
        } catch {
          fail(token, "This browser could not start audio at the negotiated rate. Enable audio again to retry.");
          return false;
        } finally {
          if (token === epoch) setup = null;
          else await closeContext(candidate);
        }
      })();
      changed(); // Loading a running context can be cancelled by the next click.
      return setup;
    } catch {
      fail(token, "This browser does not support the negotiated audio rate or AudioWorklet.");
      return Promise.resolve(false);
    }
  };
  return {
    get state() { return state(); },
    configure(value) {
      const selected = parseConfig(value);
      if (selected === null) {
        void teardown();
        config = {status: "unavailable", generation: 0n};
        explanation = "The server sent an invalid audio configuration.";
        changed();
        return Promise.resolve(false);
      }
      if (selected.status === config.status && selected.generation === config.generation &&
        selected.sampleRate === config.sampleRate && selected.channels === config.channels) return start();
      if (playing && node !== null && context?.state === "running" && config.status === "available" &&
        selected.status === "available" && selected.sampleRate === config.sampleRate && selected.channels === config.channels) {
        config = selected;
        try {
          // MessagePort order flushes old samples before new-generation PCM.
          // Retain an old transfer's tag until its ACK: reset cannot recall a
          // posted buffer, and the bridge must still hold at most one buffer.
          node.port.postMessage({type: "reset", generation: selected.generation});
        } catch {
          fail(epoch, "Audio delivery stopped. Enable audio again to retry.");
          return Promise.resolve(false);
        }
        explanation = "";
        changed();
        return Promise.resolve(true);
      }
      void teardown();
      config = selected;
      explanation = "";
      changed();
      return start();
    },
    setEnabled(value) {
      enabled = Boolean(value);
      if (!enabled) {
        const closed = teardown();
        explanation = "";
        changed();
        return closed.then(() => false);
      }
      changed();
      if (setup !== null && context?.state === "suspended") {
        const pending = setup, candidate = context, token = epoch;
        // An autoplay-blocked resume may remain pending. Retry synchronously
        // in this fresh gesture while preserving the existing setup and node.
        return (async () => {
          try {
            await candidate.resume();
            return await pending;
          } catch {
            fail(token, "Audio playback could not resume. Enable audio again to retry.");
            return false;
          }
        })();
      }
      return start();
    },
    push(frame) {
      if (!playing || context?.state !== "running" || inFlight !== null || frame?.generation !== config.generation) return false;
      try {
        // Bracket the wall-clock sample: a scheduling pause inside this pair
        // must not hide that pause from the worklet's elapsed-time fallback.
        const before = performance.now();
        const postedWallMS = Date.now();
        const after = performance.now();
        if (!Number.isFinite(before) || !Number.isFinite(after) || !Number.isFinite(postedWallMS) ||
          !Number.isFinite(performance.timeOrigin) || after < before || after - before > 5) return false;
        const postedAtMS = performance.timeOrigin + before + (after - before) / 2;
        const block = trimAudioBlock(frame, config.sampleRate, config.channels);
        if (block === null) return false;
        inFlight = {generation: block.generation, sequence: block.sequence};
        node.port.postMessage({block, postedAtMS, postedWallMS}, [block.payload.buffer]);
        return true;
      } catch {
        fail(epoch, "Audio delivery stopped. Enable audio again to retry.");
        return false;
      }
    },
    reset() {
      const closed = teardown();
      config = {status: "unavailable", generation: 0n};
      explanation = "";
      changed();
      return closed;
    },
  };
}
