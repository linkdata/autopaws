import assert from "node:assert/strict";
import test from "node:test";
import {AUDIO_QUEUE_MS, createAudioQueue, trimAudioBlock} from "./assets/viewer-helpers.mjs";
import {createAudioPlayback} from "./assets/audio-playback.mjs";

const sourceOrigin = 1234567890123456789n;

function block({sampleRate = 1000, channels = 1, sampleFrames = 20, sequence = 0n,
  generation = 1n, timestampNS = sourceOrigin, timestampFlags = 5,
  samples = () => 16384, ...extra} = {}) {
  const payload = new Uint8Array(sampleFrames * channels * 2);
  const view = new DataView(payload.buffer);
  for (let frame = 0; frame < sampleFrames; frame++) {
    for (let channel = 0; channel < channels; channel++) view.setInt16((frame * channels + channel) * 2, samples(frame, channel), true);
  }
  return {kind: 2, generation, sequence, timestampNS, timestampFlags, sampleFrames,
    durationNS: Number(BigInt(sampleFrames) * 1000000000n / BigInt(sampleRate)), payload, ...extra};
}

function queue(options = {}) { return createAudioQueue({sampleRate: 1000, channels: 1, generation: 1n, ...options}); }

test("mono S16LE conversion preserves sign/extrema and outputs silence on underrun", () => {
  const audio = queue();
  assert.equal(AUDIO_QUEUE_MS, 50);
  assert.equal(audio.generation, 1n);
  assert.equal(audio.push(block({sampleFrames: 5, samples: frame => [-32768, -16384, 0, 16384, 32767][frame]}), 0), true);
  const output = new Float32Array(8).fill(42);
  assert.equal(audio.render([output], 0), 5);
  assert.deepEqual([...output], [-1, -0.5, 0, 0.5, 32767 / 32768, 0, 0, 0]);
  output.fill(1);
  assert.equal(audio.render([output], 1), 0);
  assert.deepEqual([...output], Array(8).fill(0));
  assert.equal(audio.queuedFrames, 0);
});

test("stereo samples are deinterleaved independently at the negotiated rate", () => {
  const audio = queue({sampleRate: 96000, channels: 2});
  const pcm = block({sampleRate: 96000, channels: 2, sampleFrames: 3,
    samples: (frame, channel) => channel === 0 ? 16384 + frame : -16384 - frame});
  assert.equal(audio.push(pcm, 8000), true);
  const left = new Float32Array(4), right = new Float32Array(4);
  assert.equal(audio.render([left, right], 8000), 3);
  assert.deepEqual([...left], [0.5, 16385 / 32768, 16386 / 32768, 0]);
  assert.deepEqual([...right], [-0.5, -16385 / 32768, -16386 / 32768, 0]);
  assert.equal(audio.capacityFrames, 4800);
});

test("overflow retains the newest samples, with fixed frame and duration bounds", () => {
  const audio = queue();
  audio.push(block({sampleFrames: 40, samples: frame => frame}), 0);
  audio.push(block({sampleFrames: 40, sequence: 1n, timestampNS: sourceOrigin + 40000000n,
    samples: frame => 40 + frame}), 1);
  assert.equal(audio.queuedFrames, 50);
  assert.equal(audio.durationMS, 50);
  const output = new Float32Array(50);
  audio.render([output], 1);
  assert.deepEqual([...output], Array.from({length: 50}, (_, frame) => (30 + frame) / 32768));
  const moving = queue();
  for (let i = 0; i < 10000; i++) {
    assert.equal(moving.push(block({sampleFrames: 1, sequence: BigInt(i), timestampNS: sourceOrigin + BigInt(i) * 1000000n}), i), true);
    if (i % 3 === 0) moving.render([new Float32Array(1)], i);
    assert.ok(moving.queuedFrames <= 50 && moving.durationMS <= 50);
  }
});

test("overlapping capture estimates cannot overflow the ring and evict oldest samples first", () => {
  const audio = queue();
  audio.push(block({sampleFrames: 50, samples: () => 1}), 0);
  audio.push(block({sampleFrames: 1, sequence: 1n, timestampNS: sourceOrigin + 49000000n,
    samples: () => 2}), 0);
  assert.equal(audio.queuedFrames, 50);
  const output = new Float32Array(50);
  audio.render([output], 0);
  assert.deepEqual([...output], [...Array(49).fill(1 / 32768), 2 / 32768]);
});

test("partially overlapping capture estimates cannot hide stale samples behind a fresh prefix", () => {
  const audio = queue();
  audio.push(block({sampleFrames: 20, samples: () => 100}), 0);
  audio.push(block({sampleFrames: 20, sequence: 1n, timestampNS: sourceOrigin + 10000000n,
    samples: () => 200}), 0);
  // Source-now65ms gives a15ms cutoff. The first block has5 fresh samples;
  // the second has15, with5 stale samples hidden behind the first's tail.
  const output = new Float32Array(50).fill(42);
  assert.equal(audio.render([output], 35), 20);
  assert.deepEqual([...output], [
    ...Array(5).fill(100 / 32768), ...Array(15).fill(200 / 32768), ...Array(30).fill(0),
  ]);
  assert.equal(audio.queuedFrames, 0);
});

test("an oversized block drops its old prefix rather than enlarging the ring", () => {
  const audio = queue();
  audio.push(block({sampleFrames: 100, samples: frame => frame}), 0);
  assert.equal(audio.queuedFrames, 50);
  const output = new Float32Array(50);
  audio.render([output], 0);
  assert.deepEqual([...output], Array.from({length: 50}, (_, frame) => (50 + frame) / 32768));
});

test("local elapsed time expires old source audio and a delayed transport burst", () => {
  const audio = queue();
  audio.push(block(), 7000);
  const output = new Float32Array(50).fill(1);
  assert.equal(audio.render([output], 7500), 0);
  assert.ok(output.every(sample => sample === 0));
  for (let i = 1; i <= 5; i++) {
    assert.equal(audio.push(block({sequence: BigInt(i), timestampNS: sourceOrigin + BigInt(i) * 20000000n}), 7500), false);
  }
  assert.equal(audio.queuedFrames, 0);
  assert.equal(audio.push(block({sequence: 25n, timestampNS: sourceOrigin + 500000000n,
    samples: () => -16384}), 7500), true);
  assert.equal(audio.render([output], 7500), 20);
  assert.ok(output.slice(0, 20).every(sample => sample === -0.5));
});

test("shared monotonic posting time expires a port-delayed first block before anchoring", () => {
  const audio = queue();
  assert.equal(audio.push(block(), 100, 0), false);
  assert.equal(audio.queuedFrames, 0);
  assert.equal(audio.push(block({sequence: 1n}), 100, 100), true);
  const output = new Float32Array(20);
  assert.equal(audio.render([output], 100), 20);
  assert.equal(audio.push(block({sequence: 2n, timestampNS: sourceOrigin + 20000000n}), 130, 100), true);
  assert.equal(audio.render([output], 131), 19); // One sample aged beyond 50ms.
});

test("clock rollback flushes PCM but preserves source time and sequence rejection", () => {
  const audio = queue();
  const output = new Float32Array(20);
  audio.push(block(), 1000);
  assert.equal(audio.render([new Float32Array(1)], 999), 1, "timer quantization must not flush the ring");
  assert.equal(audio.queuedFrames, 19);
  assert.equal(audio.render([output], 1500), 0);
  assert.equal(audio.render([output], 500), 0, "a backward wall step rebases the fallback clock");
  assert.equal(audio.queuedFrames, 0);
  assert.equal(audio.push(block(), 1500), false, "a clock correction must not permit replay");
  assert.equal(audio.push(block({sequence: 1n, timestampNS: sourceOrigin + 20000000n}), 1500), false,
    "a new sequence captured 480ms ago cannot establish a fresh source anchor");
  assert.equal(audio.queuedFrames, 0);
  assert.equal(audio.push(block({sequence: 25n, timestampNS: sourceOrigin + 500000000n}), 1500), true);
  assert.equal(audio.render([output], 1500), 20, "fresh source time resumes after the corrected clock pair");
  assert.ok(output.every(sample => sample === 0.5));
});

test("generation reset clears all audio, ordering and timing anchors", () => {
  const audio = queue();
  const first = block({sequence: 9007199254740993n});
  assert.equal(audio.push(first, 0), true);
  assert.equal(audio.push(first, 0), false);
  assert.equal(audio.push(block({sequence: 1n}), 0), false);
  audio.reset(2n);
  assert.equal(audio.queuedFrames, 0);
  assert.equal(audio.generation, 2n);
  assert.equal(audio.push(block(), 1000), false);
  assert.equal(audio.push(block({generation: 2n}), 1000), true);
  audio.reset();
  assert.equal(audio.generation, 2n);
  assert.equal(audio.queuedFrames, 0);
});

test("same-source generation reset retains timing to reject backlogged first PCM", () => {
  const audio = queue();
  const output = new Float32Array(20);
  assert.equal(audio.push(block({sequence: 77n}), 1000), true);
  audio.reset(2n, {preserveClock: true});
  assert.equal(audio.generation, 2n);
  assert.equal(audio.queuedFrames, 0);
  assert.equal(audio.render([output], 1500), 0);
  assert.equal(audio.push(block({generation: 2n, timestampNS: sourceOrigin + 20000000n}), 1500), false,
    "reset sequence zero cannot reanchor PCM captured 480ms ago");
  assert.equal(audio.push(block({generation: 2n, sequence: 25n, timestampNS: sourceOrigin + 500000000n}), 1500), true);
  assert.equal(audio.render([output], 1500), 20);
  assert.ok(output.every(sample => sample === 0.5));
});

test("invalid formats/PCM/timestamps never enter the ring", () => {
  for (const options of [{sampleRate: 0}, {sampleRate: Infinity}, {sampleRate: 1},
    {sampleRate: 100000000}, {channels: 0}, {channels: 3}, {generation: 1}, {generation: -1n}]) {
    assert.throws(() => queue(options), RangeError);
  }
  const audio = queue();
  for (const pcm of [null, block({kind: 1}), block({generation: 2n}),
    block({sequence: -1n}), block({sequence: 1n << 64n}), block({timestampNS: -1n}),
    block({timestampFlags: 0}), block({timestampFlags: 3}), block({timestampFlags: 9}),
    block({timestampFlags: -4294967295}), block({timestampFlags: 4294967301}),
    block({sampleFrames: 0}), block({durationNS: 1}), block({payload: new Uint8Array(1)}),
    block({payload: new Int16Array(20)}), {...block(), sampleFrames: (1 << 20) + 1},
  ]) assert.equal(audio.push(pcm, 0), false);
  assert.equal(audio.push(block(), NaN), false);
  assert.equal(audio.push(block(), 0, Infinity), false);
  assert.equal(audio.push(block(), 0, 1), false);
  assert.equal(audio.queuedFrames, 0);
  assert.throws(() => audio.reset(1), RangeError);
  assert.equal(audio.render([], 0), 0);
  assert.equal(audio.render([new Float32Array(1)], NaN), 0);
  assert.equal(audio.render([new Int16Array(1)], 0), 0);
  const stereo = queue({channels: 2});
  assert.equal(stereo.render([new Float32Array(1), new Float32Array(2)], 0), 0);
});

test("transfer trimming owns only the latest50ms and updates exact source metadata", () => {
  const original = block({sampleFrames: 100, samples: frame => frame});
  const trimmed = trimAudioBlock(original, 1000, 1);
  assert.equal(trimmed.sampleFrames, 50);
  assert.equal(trimmed.durationNS, 50000000);
  assert.equal(trimmed.timestampNS, sourceOrigin + 50000000n);
  assert.equal(trimmed.payload.byteLength, 100);
  assert.equal(trimmed.payload.buffer.byteLength, 100);
  assert.equal(new DataView(trimmed.payload.buffer).getInt16(0, true), 50);
  original.payload.fill(255);
  assert.equal(new DataView(trimmed.payload.buffer).getInt16(0, true), 50);
  const small = block();
  assert.notEqual(trimAudioBlock(small, 1000, 1).payload.buffer, small.payload.buffer);
  assert.equal(trimAudioBlock(block({durationNS: 0}), 1000, 1), null);
  assert.equal(trimAudioBlock(block({sampleFrames: 100, timestampNS: (1n << 64n) - 1n}), 1000, 1), null);
});

function deferred() {
  let resolve, reject;
  const promise = new Promise((accept, fail) => { resolve = accept; reject = fail; });
  return {promise, resolve, reject};
}

function environment(options = {}) {
  const contexts = [], nodes = [], events = [], states = [];
  class FakeContext extends EventTarget {
    constructor({sampleRate}) {
      super();
      if (options.constructorError) throw new Error("rate unsupported");
      events.push("new");
      contexts.push(this);
      this.sampleRate = options.actualRate ?? sampleRate;
      this.currentTime = 12.5;
      this.state = "suspended";
      this.destination = {};
      this.closeCount = 0;
      this.resumeCalls = 0;
      this.pendingResume = null;
      const load = options.loads?.[contexts.length - 1] ?? options.load;
      if (!options.missingWorklet) this.audioWorklet = {addModule: url => {
        events.push("load");
        assert.equal(String(url), "/audio-worklet.mjs");
        if (options.syncLoadError) throw new Error("module request failed");
        return (async () => {
          if (options.loadError) throw new Error("module failed");
          if (load) await load.promise;
        })();
      }};
    }
    resume() {
      events.push("resume");
      this.resumeCalls++;
      if (options.deferResumeUntilRetry) {
        if (this.resumeCalls === 1) {
          this.pendingResume = deferred();
          return this.pendingResume.promise;
        }
        if (options.retryError === "throw") throw new Error("resume threw");
        if (options.retryError === "reject") return Promise.reject(new Error("resume rejected"));
        this.pendingResume.resolve();
      }
      if (options.resumeError) return Promise.reject(new Error("resume denied"));
      this.state = options.staysSuspended ? "suspended" : "running";
      return options.resume?.promise ?? Promise.resolve();
    }
    close() {
      this.closeCount++;
      this.state = "closed";
      this.pendingResume?.reject(new Error("context closed"));
      this.dispatchEvent(new Event("statechange"));
      if (options.closeThrows) throw new Error("close failed");
      return options.closeRejects ? Promise.reject(new Error("close failed")) : Promise.resolve();
    }
  }
  class FakeNode extends EventTarget {
    constructor(context, name, config) {
      super();
      if (options.nodeError) throw new Error("node failed");
      assert.equal(name, "autopaws-audio");
      assert.equal(context.sampleRate, config.processorOptions.sampleRate);
      this.options = config;
      this.messages = [];
      this.port = {
        postMessage: (message, transfers) => {
          if (options.sendError) throw new Error("port closed");
          this.messages.push(structuredClone(message, {transfer: transfers}));
        },
        close: () => { this.closed = true; },
      };
      nodes.push(this);
    }
    connect() { if (options.connectError) throw new Error("connect failed"); }
    disconnect() { this.disconnected = true; }
    ack(sequence, generation = this.options.processorOptions.generation) {
      this.port.onmessage({data: {type: "ack", generation, sequence}});
    }
  }
  const player = createAudioPlayback({AudioContext: FakeContext, AudioWorkletNode: FakeNode,
    secureContext: true, workletURL: "/audio-worklet.mjs", onState: state => states.push(state), ...options.capabilities});
  return {player, contexts, nodes, events, states};
}

function available(overrides = {}) {
  return {status: "available", sampleRate: 48000, channels: 2, generation: "9007199254740993", ...overrides};
}

function playbackBlock(overrides = {}) {
  return block({sampleRate: 48000, channels: 2, sampleFrames: 960, generation: 9007199254740993n, ...overrides});
}

test("a user gesture requests/resumes the negotiated rate, not a device preferred rate", async () => {
  for (const [rate, channels] of [[48000, 2], [96000, 1], [44100, 2]]) {
    const {player, contexts, nodes, events} = environment();
    assert.equal(await player.configure(available({sampleRate: rate, channels})), false);
    assert.equal(contexts.length, 0);
    const started = player.setEnabled(true);
    assert.deepEqual(events, ["new", "resume"]);
    assert.equal(await started, true);
    assert.equal(contexts[0].sampleRate, rate);
    assert.deepEqual(nodes[0].options.outputChannelCount, [channels]);
    assert.deepEqual(events, ["new", "resume", "load"]);
    assert.deepEqual(player.state, {status: "available", enabled: true, playing: true, retryable: false, explanation: ""});
    assert.equal(await player.configure(available({sampleRate: rate, channels})), true);
    assert.equal(contexts.length, 1);
    await player.reset();
  }
});

test("a fresh enable gesture retries a pending suspended resume without another context", async () => {
  const {player, contexts, nodes} = environment({deferResumeUntilRetry: true});
  await player.setEnabled(true); // Preserve the desired choice while disconnected.
  const pending = player.configure(available());
  await Promise.resolve();
  assert.equal(contexts[0].state, "suspended");
  assert.equal(player.state.retryable, true);
  assert.equal(contexts[0].resumeCalls, 1);
  assert.equal(player.push(playbackBlock()), false);
  const retry = player.setEnabled(true);
  assert.equal(contexts[0].resumeCalls, 2, "resume must run before the fresh gesture returns");
  assert.equal(contexts.length, 1);
  assert.equal(await retry, true);
  assert.equal(await pending, true);
  assert.equal(nodes.length, 1);
  assert.equal(player.state.playing, true);
  await player.reset();
});

test("pending-resume retry exceptions and rejections close the one context and stay audio-only", async () => {
  for (const retryError of ["throw", "reject"]) {
    const {player, contexts, nodes} = environment({deferResumeUntilRetry: true, retryError});
    await player.setEnabled(true);
    const pending = player.configure(available());
    await Promise.resolve();
    assert.equal(await player.setEnabled(true), false);
    assert.equal(await pending, false);
    assert.equal(contexts.length, 1);
    assert.equal(contexts[0].state, "closed");
    assert.equal(nodes.length, 0);
    assert.equal(player.state.enabled, true);
    assert.equal(player.state.playing, false);
    assert.match(player.state.explanation, /resume/);
    await player.reset();
  }
});

test("ordinary module loading is cancellable and late completion cannot unmute", async () => {
  const load = deferred();
  const {player, contexts, nodes, states} = environment({load});
  await player.configure(available());
  const pending = player.setEnabled(true);
  assert.equal(contexts[0].state, "running");
  assert.equal(player.state.enabled, true);
  assert.equal(player.state.playing, false);
  assert.equal(player.state.retryable, false);
  assert.equal(states.at(-1).retryable, false, "notify the page after starting ordinary loading");
  await player.setEnabled(false);
  load.resolve();
  assert.equal(await pending, false);
  assert.equal(contexts.length, 1);
  assert.equal(contexts[0].state, "closed");
  assert.equal(nodes.length, 0);
  assert.equal(player.state.enabled, false);
  assert.equal(player.state.retryable, false);
  assert.equal(player.state.playing, false);
});

test("startup context state changes update retry versus cancel without stale epoch callbacks", async () => {
  const load = deferred();
  const {player, contexts, states} = environment({load});
  await player.configure(available());
  const pending = player.setEnabled(true);
  contexts[0].state = "suspended";
  contexts[0].dispatchEvent(new Event("statechange"));
  assert.equal(states.at(-1).retryable, true);
  contexts[0].state = "running";
  contexts[0].dispatchEvent(new Event("statechange"));
  assert.equal(states.at(-1).retryable, false);
  await player.setEnabled(false);
  const notifications = states.length;
  contexts[0].dispatchEvent(new Event("statechange"));
  assert.equal(states.length, notifications, "a closed generation must not change the current controls");
  load.resolve();
  assert.equal(await pending, false);
});

test("one acknowledged <=50ms transfer bounds port storage and drops busy arrivals", async () => {
  const {player, nodes} = environment();
  await player.configure(available());
  await player.setEnabled(true);
  const pcm = playbackBlock({sampleFrames: 4800});
  assert.equal(player.push(pcm), true);
  const message = nodes[0].messages[0];
  assert.equal(message.block.sampleFrames, 2400);
  assert.equal(message.block.payload.buffer.byteLength, 2400 * 2 * 2);
  assert.ok(Number.isFinite(message.postedAtMS));
  assert.ok(Number.isFinite(message.postedWallMS));
  assert.equal(pcm.payload.byteLength, 4800 * 2 * 2); // Caller's wire buffer remains usable.
  assert.equal(player.push(playbackBlock({sequence: 1n})), false);
  nodes[0].ack(1n);
  nodes[0].ack(0n, 3n);
  nodes[0].port.onmessage({data: {type: "unknown"}});
  nodes[0].port.onmessage({data: null});
  assert.equal(player.push(playbackBlock({sequence: 1n})), false);
  nodes[0].ack(0n);
  assert.equal(player.push(playbackBlock({sequence: 1n})), true);
  assert.equal(nodes[0].messages.length, 2);
  await player.reset();
});

function pageClock(t, {timeOrigin = 1700000000000, times = [1000, 1000], wall = 1800000000000} = {}) {
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, "performance");
  const clock = {timeOrigin, times, wall};
  Object.defineProperty(globalThis, "performance", {configurable: true, value: {
    get timeOrigin() { return clock.timeOrigin; },
    now: () => clock.times.length > 1 ? clock.times.shift() : clock.times[0],
  }});
  t.mock.method(Date, "now", () => clock.wall);
  t.after(() => Object.defineProperty(globalThis, "performance", descriptor));
  return clock;
}

test("page clock pairs bracket Date without using the frozen audio rendering timeline", async t => {
  pageClock(t, {times: [1000, 1002]});
  const {player, contexts, nodes} = environment();
  await player.configure(available());
  await player.setEnabled(true);
  assert.equal(player.push(playbackBlock()), true);
  assert.equal(contexts[0].currentTime, 12.5);
  const {postedAtMS, postedWallMS} = nodes[0].messages[0];
  assert.equal(postedAtMS, 1700000001001);
  assert.equal(postedWallMS, 1800000000000);
  await player.reset();
});

test("a paused or invalid clock sample drops the transfer without occupying the ACK slot", async t => {
  const clock = pageClock(t);
  const {player, nodes} = environment();
  await player.configure(available());
  await player.setEnabled(true);
  for (const times of [[1000, 1500], [1000, 999], [Infinity, Infinity], [1000, NaN]]) {
    clock.times = times;
    assert.equal(player.push(playbackBlock()), false);
    assert.equal(nodes[0].messages.length, 0);
  }
  clock.times = [2000, 2000];
  clock.wall = NaN;
  assert.equal(player.push(playbackBlock()), false);
  clock.wall = 1800000001000;
  clock.timeOrigin = NaN;
  assert.equal(player.push(playbackBlock()), false);
  clock.timeOrigin = 1700000000000;
  assert.equal(player.push(playbackBlock()), true, "a skipped transfer needs no ACK before retry");
  assert.equal(nodes[0].messages.length, 1);
  await player.reset();
});

test("mute/reconnect/off preserve the user's choice and never retain PCM", async () => {
  const {player, contexts, nodes} = environment();
  await player.setEnabled(true);
  assert.equal(contexts.length, 0);
  await player.configure(available());
  player.push(playbackBlock());
  await player.configure({status: "off", generation: "2"});
  assert.equal(player.state.enabled, true);
  assert.equal(player.state.status, "off");
  assert.equal(nodes[0].closed, true);
  assert.equal(player.push(playbackBlock()), false);
  await player.reset();
  assert.equal(player.state.enabled, true);
  await player.configure(available({generation: "3", channels: 1, sampleRate: 96000}));
  assert.equal(contexts.length, 2);
  assert.equal(contexts[1].sampleRate, 96000);
  assert.equal(player.state.playing, true);
  await player.setEnabled(false);
  await player.configure(available({generation: "4"}));
  assert.equal(contexts.length, 2);
  assert.equal(player.state.enabled, false);
  assert.equal(player.push(playbackBlock({generation: 4n})), false);
});

test("missing capabilities and unsupported rates fail gracefully", async () => {
  for (const options of [
    {capabilities: {secureContext: false}}, {capabilities: {AudioContext: undefined}},
    {capabilities: {AudioWorkletNode: undefined}}, {missingWorklet: true},
    {actualRate: 44100}, {constructorError: true}, {loadError: true},
    {syncLoadError: true}, {syncLoadError: true, resumeError: true},
    {resumeError: true}, {staysSuspended: true}, {nodeError: true}, {connectError: true},
  ]) {
    const {player, contexts} = environment(options);
    await player.configure(available());
    assert.equal(await player.setEnabled(true), false);
    assert.equal(player.state.playing, false);
    assert.equal(player.state.enabled, true);
    assert.notEqual(player.state.explanation, "");
    assert.equal(player.push(playbackBlock()), false);
    assert.ok(contexts.every(context => context.state === "closed"));
  }
});

test("an old rejected setup cannot replace a newer successful context or its explanation", async () => {
  const oldLoad = deferred();
  const {player, contexts, nodes} = environment({loads: [oldLoad]});
  await player.configure(available());
  const old = player.setEnabled(true);
  await Promise.resolve();
  assert.equal(await player.configure(available({generation: "9007199254740994"})), true);
  assert.equal(player.state.playing, true);
  oldLoad.reject(new Error("obsolete module failed"));
  assert.equal(await old, false);
  assert.equal(player.state.playing, true);
  assert.equal(player.state.explanation, "");
  assert.equal(contexts[0].state, "closed");
  assert.equal(contexts[1].state, "running");
  assert.equal(nodes.length, 1);
  await player.reset();
});

test("a missing-capability page needs neither a callback nor browser audio globals", async () => {
  const player = createAudioPlayback({secureContext: false});
  await player.configure(available());
  assert.equal(await player.setEnabled(true), false);
  assert.equal(player.state.playing, false);
  assert.match(player.state.explanation, /secure context/);
  await player.reset();
});

test("no PCM enters while setup is unresolved, or after mute supersedes it", async () => {
  const load = deferred();
  const {player, contexts, nodes} = environment({load});
  await player.configure(available());
  const setup = player.setEnabled(true);
  assert.equal(player.setEnabled(true), setup);
  assert.equal(contexts.length, 1);
  assert.equal(player.push(playbackBlock()), false);
  await player.setEnabled(false);
  load.resolve();
  assert.equal(await setup, false);
  assert.equal(nodes.length, 0);
  assert.equal(contexts[0].closeCount, 1);
  assert.equal(player.state.enabled, false);
  assert.equal(player.state.explanation, "");
});

test("late setup resolution/rejection cannot overwrite a new generation", async () => {
  for (const reject of [false, true]) {
    const load = deferred();
    const {player, contexts, nodes} = environment({load});
    await player.configure(available());
    const old = player.setEnabled(true);
    await Promise.resolve();
    const next = player.configure(available({generation: "9007199254740994", sampleRate: 96000, channels: 1}));
    if (reject) load.reject(new Error("old load failed"));
    else load.resolve();
    assert.equal(await old, false);
    assert.equal(await next, !reject);
    assert.equal(contexts[0].state, "closed");
    if (!reject) {
      assert.equal(nodes.length, 1);
      assert.equal(nodes[0].options.processorOptions.generation, 9007199254740994n);
      assert.equal(player.state.playing, true);
      assert.equal(player.state.explanation, "");
    }
    await player.reset();
  }
});

test("same-format recovery resets in order without recreating the running graph or bypassing the transfer bound", async () => {
  for (const pendingTransfer of [false, true]) {
    const {player, nodes, contexts} = environment();
    await player.configure(available());
    await player.setEnabled(true);
    const node = nodes[0];
    const oldGeneration = 9007199254740993n, generation = oldGeneration + 1n;
    if (pendingTransfer) assert.equal(player.push(playbackBlock()), true);
    assert.equal(await player.configure(available({generation: String(generation)})), true);
    assert.equal(contexts.length, 1);
    assert.equal(nodes.length, 1);
    assert.equal(contexts[0].state, "running");
    assert.equal(contexts[0].resumeCalls, 1, "routine ALSA recovery must not require another resume gesture");
    assert.equal(contexts[0].closeCount, 0);
    assert.equal(node.closed, undefined);
    assert.equal(player.state.playing, true);
    assert.deepEqual(node.messages.at(-1), {type: "reset", generation});
    assert.equal(player.push(playbackBlock({sequence: 1n})), false, "old PCM cannot enter the recovered generation");
    const currentPCM = playbackBlock({generation});
    if (pendingTransfer) {
      assert.equal(player.push(currentPCM), false, "old posted PCM still owns the sole transfer slot");
      node.ack(0n, oldGeneration);
    }
    assert.equal(player.push(currentPCM), true);
    const offset = pendingTransfer ? 1 : 0;
    assert.deepEqual(node.messages[offset], {type: "reset", generation});
    assert.equal(node.messages[offset + 1].block.generation, generation);
    node.ack(0n, oldGeneration);
    assert.equal(player.push({...currentPCM, sequence: 1n}), false, "a late old ACK cannot release a new tag with the same sequence");
    node.ack(0n, generation);
    assert.equal(player.push({...currentPCM, sequence: 1n}), true);
    await player.reset();
  }
});

test("a generation-reset delivery failure closes the graph and remains retryable", async () => {
  const {player, contexts} = environment({sendError: true});
  await player.configure(available());
  await player.setEnabled(true);
  assert.equal(await player.configure(available({generation: "9007199254740994"})), false);
  assert.equal(contexts.length, 1);
  assert.equal(contexts[0].state, "closed");
  assert.equal(player.state.enabled, true);
  assert.equal(player.state.playing, false);
  assert.equal(player.state.retryable, true);
  assert.match(player.state.explanation, /delivery/);
  await player.reset();
});

test("format changes close old nodes and ignore old acknowledgments/errors", async () => {
  const {player, nodes, contexts} = environment();
  await player.configure(available());
  await player.setEnabled(true);
  player.push(playbackBlock());
  await player.configure(available({generation: "9007199254740994", sampleRate: 96000}));
  const old = nodes[0], current = nodes[1];
  assert.equal(old.closed, true);
  assert.equal(old.disconnected, true);
  assert.equal(contexts[0].state, "closed");
  assert.equal(player.push(playbackBlock()), false);
  const currentPCM = playbackBlock({generation: 9007199254740994n, sampleRate: 96000, sampleFrames: 1920});
  assert.equal(player.push(currentPCM), true);
  old.ack(0n);
  old.dispatchEvent(new Event("processorerror"));
  assert.equal(player.state.playing, true);
  assert.equal(player.push({...currentPCM, sequence: 1n}), false);
  current.ack(0n);
  assert.equal(player.push({...currentPCM, sequence: 1n}), true);
  await player.reset();
});

test("unexpected context suspension flushes the old ring and allows a fresh gesture retry", async () => {
  const {player, contexts, nodes} = environment();
  await player.configure(available());
  await player.setEnabled(true);
  player.push(playbackBlock());
  contexts[0].state = "suspended";
  assert.equal(player.push(playbackBlock({sequence: 1n})), false);
  contexts[0].dispatchEvent(new Event("statechange"));
  assert.equal(player.state.enabled, true);
  assert.equal(player.state.playing, false);
  assert.equal(nodes[0].closed, true);
  assert.match(player.state.explanation, /interrupted/);
  assert.equal(player.push(playbackBlock({sequence: 1n})), false);
  assert.equal(await player.setEnabled(true), true);
  assert.equal(contexts.length, 2);
  assert.equal(player.push(playbackBlock({sequence: 2n})), true);
  await player.reset();
});

test("processor/port failures stay local to audio and rejected close promises are observed", async () => {
  for (const options of [{sendError: true}, {closeRejects: true}, {closeThrows: true}]) {
    const {player, nodes} = environment(options);
    await player.configure(available());
    await player.setEnabled(true);
    if (options.sendError) assert.equal(player.push(playbackBlock()), false);
    else nodes[0].dispatchEvent(new Event("processorerror"));
    assert.equal(player.state.playing, false);
    assert.equal(player.state.enabled, true);
    assert.notEqual(player.state.explanation, "");
    await player.reset();
  }
});

test("invalid configs/PCM are rejected without an exception escaping into the page", async () => {
  const {player, nodes} = environment();
  for (const value of [null, {}, {status: "unknown", generation: "1"},
    available({generation: 1}), available({generation: "-1"}), available({generation: "+1"}),
    available({generation: String(1n << 64n)}), available({sampleRate: 0}),
    available({sampleRate: 1.5}), available({channels: 3})]) {
    assert.equal(await player.configure(value), false);
    assert.equal(player.state.status, "unavailable");
  }
  await player.configure(available());
  await player.setEnabled(true);
  assert.equal(player.push(playbackBlock({durationNS: 1})), false);
  assert.equal(nodes[0].messages.length, 0);
  await player.reset();
});

let WorkletProcessor;

async function workletEnvironment(t, {nativeClock = false} = {}) {
  const clock = {mono: 1000, wall: 1800000000000, timeOrigin: 1700000000000};
  const globals = {
    AudioWorkletProcessor: class {
      constructor() { this.port = {messages: [], postMessage(message) { this.messages.push(message); }}; }
    },
    sampleRate: 48000,
    currentTime: 1, // Deliberately frozen in every realtime-aging test.
    performance: nativeClock === true ? {timeOrigin: clock.timeOrigin, now: () => clock.mono} :
      nativeClock === "partial" ? {now: () => { assert.fail("an unshared monotonic epoch cannot be used"); }} : undefined,
    registerProcessor: (name, constructor) => { assert.equal(name, "autopaws-audio"); WorkletProcessor = constructor; },
  };
  const saved = new Map(Object.keys(globals).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]));
  for (const [key, value] of Object.entries(globals)) {
    Object.defineProperty(globalThis, key, {value, configurable: true, writable: true});
  }
  t.mock.method(Date, "now", () => clock.wall);
  t.after(() => {
    for (const [key, descriptor] of saved) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor);
      else delete globalThis[key];
    }
  });
  await import("./assets/audio-worklet.mjs");
  const create = () => new WorkletProcessor({processorOptions: {sampleRate: 48000, channels: 1, generation: 1n}});
  const stamp = () => ({postedAtMS: clock.timeOrigin + clock.mono, postedWallMS: clock.wall});
  const send = (processor, pcm, posted = stamp()) => processor.port.onmessage({data: {block: pcm, ...posted}});
  const advance = ms => { clock.mono += ms; clock.wall += ms; };
  const render = (processor, frames = 256) => {
    const output = new Float32Array(frames).fill(42);
    assert.equal(processor.process([], [[output]]), true);
    return output;
  };
  return {clock, Processor: WorkletProcessor, create, stamp, send, advance, render};
}

test("worklet renders PCM/silence and acknowledges expired first transfers despite a frozen audio clock", async t => {
  const h = await workletEnvironment(t);
  assert.throws(() => new h.Processor({processorOptions: {sampleRate: 44100}}), RangeError);
  const processor = h.create();
  assert.ok(h.render(processor).every(sample => sample === 0), "no clock pair means silence before first PCM");
  const pcm = block({sampleRate: 48000, sampleFrames: 128});
  const oldStamp = h.stamp();
  h.advance(500);
  h.send(processor, pcm, oldStamp);
  assert.deepEqual(processor.port.messages, [{type: "ack", generation: 1n, sequence: 0n}]);
  assert.ok(h.render(processor).every(sample => sample === 0), "a delayed first block must not establish a fresh anchor");
  h.send(processor, {...pcm, sequence: 1n});
  const output = h.render(processor);
  assert.ok(output.slice(0, 128).every(sample => sample === 0.5));
  assert.ok(output.slice(128).every(sample => sample === 0));
  processor.port.onmessage({data: null});
  assert.equal(processor.process([], []), true);
});

test("worklet real elapsed time expires both queued PCM and a delayed port transfer across DSP pauses", async t => {
  const h = await workletEnvironment(t);
  const processor = h.create();
  const pcm = block({sampleRate: 48000, sampleFrames: 960});
  h.send(processor, pcm);
  assert.equal(processor.queue.queuedFrames, 960);
  h.advance(500);
  assert.equal(globalThis.currentTime, 1);
  assert.ok(h.render(processor, 960).every(sample => sample === 0), "old queued PCM must age while DSP time stands still");
  const stamp = h.stamp();
  h.advance(500);
  h.send(processor, {...pcm, sequence: 1n}, stamp);
  assert.ok(h.render(processor, 960).every(sample => sample === 0));
  assert.deepEqual(processor.port.messages.at(-1), {type: "ack", generation: 1n, sequence: 1n});
  h.send(processor, {...pcm, sequence: 2n, timestampNS: sourceOrigin + 1000000000n});
  assert.ok(h.render(processor, 960).every(sample => sample === 0.5), "fresh PCM resumes without replacing the context");
});

test("fallback recovers after forward and backward wall steps without replaying old sequences", async t => {
  const h = await workletEnvironment(t);
  const processor = h.create();
  const pcm = block({sampleRate: 48000, sampleFrames: 960});
  h.send(processor, {...pcm, sequence: 10n});
  h.clock.wall += 10000;
  assert.ok(h.render(processor, 960).every(sample => sample === 0));
  h.advance(20);
  h.send(processor, {...pcm, sequence: 11n, timestampNS: sourceOrigin + 20000000n});
  assert.ok(h.render(processor, 960).every(sample => sample === 0.5), "new clock pair corrects the forward-step phase");

  const oldStamp = h.stamp();
  h.send(processor, {...pcm, sequence: 12n, timestampNS: sourceOrigin + 40000000n});
  h.clock.wall -= 20000;
  assert.ok(h.render(processor, 960).every(sample => sample === 0), "backward time flushes queued samples");
  h.send(processor, {...pcm, sequence: 13n, timestampNS: sourceOrigin + 60000000n}, oldStamp);
  assert.ok(h.render(processor, 960).every(sample => sample === 0), "a pre-step message with a future wall stamp cannot play");
  h.advance(20);
  h.send(processor, {...pcm, sequence: 14n, timestampNS: sourceOrigin + 80000000n});
  assert.ok(h.render(processor, 960).every(sample => sample === 0.5), "fresh clock pairs recover from backward steps");
  h.send(processor, {...pcm, sequence: 12n});
  assert.ok(h.render(processor, 960).every(sample => sample === 0), "rebasing must not permit replay");
  assert.equal(processor.port.messages.length, 6, "even rejected transfers release their exact ACK slot");
});

test("a genuine worklet monotonic epoch ignores wall steps and expires queued and port-delayed PCM", async t => {
  const h = await workletEnvironment(t, {nativeClock: true});
  const processor = h.create();
  const pcm = block({sampleRate: 48000, sampleFrames: 960});
  h.send(processor, pcm);
  h.clock.wall -= 10000;
  assert.ok(h.render(processor, 960).every(sample => sample === 0.5), "wall steps cannot change genuine monotonic queue time");
  const oldStamp = h.stamp();
  h.advance(500);
  h.send(processor, {...pcm, sequence: 1n}, oldStamp);
  assert.ok(h.render(processor, 960).every(sample => sample === 0));
  h.send(processor, {...pcm, sequence: 2n, timestampNS: sourceOrigin + 500000000n});
  h.clock.wall += 100000;
  h.clock.mono += 500;
  assert.ok(h.render(processor, 960).every(sample => sample === 0));
});

test("a worklet performance clock without its epoch retains the refreshed page-clock fallback", async t => {
  const h = await workletEnvironment(t, {nativeClock: "partial"});
  const processor = h.create();
  h.send(processor, block({sampleRate: 48000, sampleFrames: 960}));
  h.advance(500);
  assert.ok(h.render(processor, 960).every(sample => sample === 0));
});

test("same-format worklet reset preserves source timing and acknowledges only PCM transfers", async t => {
  const h = await workletEnvironment(t);
  const processor = h.create();
  const oldPCM = block({sampleRate: 48000, sampleFrames: 960, sequence: 77n});
  h.send(processor, oldPCM);
  h.advance(500);
  const beforeReset = processor.port.messages.length;
  processor.port.onmessage({data: {type: "reset", generation: 2n}});
  assert.equal(processor.port.messages.length, beforeReset, "reset must not acknowledge a PCM transfer");
  assert.ok(h.render(processor, 960).every(sample => sample === 0), "recovery flushes the prior generation's unplayed ring");
  h.send(processor, {...oldPCM, sequence: 78n});
  assert.ok(h.render(processor, 960).every(sample => sample === 0), "late old-generation PCM cannot refill the reset ring");
  const stalePCM = block({sampleRate: 48000, sampleFrames: 960, generation: 2n,
    timestampNS: sourceOrigin + 20000000n, samples: () => -8192});
  h.send(processor, stalePCM);
  assert.ok(h.render(processor, 960).every(sample => sample === 0), "first new-generation PCM still expires against the retained source timeline");
  assert.deepEqual(processor.port.messages.at(-1), {type: "ack", generation: 2n, sequence: 0n},
    "a stale first transfer releases its exact ACK tag");
  h.send(processor, {...stalePCM, sequence: 25n, timestampNS: sourceOrigin + 500000000n});
  assert.ok(h.render(processor, 960).every(sample => sample === -0.25));
  assert.deepEqual(processor.port.messages.at(-1), {type: "ack", generation: 2n, sequence: 25n});
  assert.equal(processor.port.messages.length, 4, "reset adds no ACK or extra transfer slot");
});
