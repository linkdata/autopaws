import {createAudioQueue} from "./viewer-helpers.mjs";

class AutopawsAudio extends AudioWorkletProcessor {
  constructor(options) {
    super();
    const config = options.processorOptions;
    if (config.sampleRate !== sampleRate) throw new RangeError("audio context rate differs from source");
    this.queue = createAudioQueue(config);
    this.clock = null;
    this.port.onmessage = event => {
      if (event.data?.type === "reset") {
        // Same-connection generations still use the server's CLOCK_MONOTONIC
        // source timeline. Keep that reference to reject backlogged recovery PCM.
        this.queue.reset(event.data.generation, {preserveClock: true});
        return;
      }
      const {block, postedAtMS, postedWallMS} = event.data ?? {};
      if (Number.isFinite(postedAtMS) && Number.isFinite(postedWallMS)) this.clock = {postedAtMS, postedWallMS};
      this.queue.push(block, this.now(), postedAtMS);
      // A stale or invalid block must release the one-transfer admission slot.
      this.port.postMessage({type: "ack", generation: block?.generation, sequence: block?.sequence});
    };
  }

  now() {
    // currentTime stops with audio rendering. Queue age must still advance
    // during that pause. Older worklets lack a native performance clock.
    if (typeof globalThis.performance?.now === "function" && Number.isFinite(performance.timeOrigin)) {
      return performance.timeOrigin + performance.now();
    }
    return this.clock === null ? NaN : this.clock.postedAtMS + (Date.now() - this.clock.postedWallMS);
  }

  process(_inputs, outputs) {
    this.queue.render(outputs[0] ?? [], this.now());
    return true;
  }
}

registerProcessor("autopaws-audio", AutopawsAudio);
