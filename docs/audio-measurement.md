# Browser audio measurement

Measure from the ALSA capture timestamp to the browser's estimated device-output
position. Queue capacity alone does not establish end-to-end latency. This
method is not an acoustic loopback measurement.

## Procedure

1. Record the build, source hashes, host/kernel, capture device, browser version,
   video size, and negotiated PCM rate, channels, period and kernel-buffer size.
   Use the same Linux host clock for capture and the measurement endpoint.
2. Enable audio with the viewer's Audio button. Keep the page visible and output
   device running. Verify video, audio progress and mute/unmute.
3. Observe the first real source sample copied into each output quantum,
   including its source timestamp and output-frame position. Quiet PCM counts
   as source data; underrun silence does not.
4. Collect at least 600 seconds of mapped output and 600 seconds of real PCM.
   Record startup and reconnect gaps separately. Exercise a bounded transport
   stall/reconnect and verify that recent audio resumes with the choice retained.
5. Save clock calibrations, mapping counts, latency distribution, engineering
   allowance, underruns, queue maximum, errors and exact instrumentation/source
   identities. Instrumentation must preserve playback and queue decisions.

The recorded lab harness is retained with the source archive identified in the
[audio result](validation/audio-2026-11-17.json). It is not a repository command.

## Clock mapping

For the first source sample copied into a quantum:

```text
sourceMS = sourceTimestampNS / 1e6 + relativeCopiedSampleMS
contextSeconds = quantumCurrentTime + firstOutputFrameIndex / sampleRate
```

Sample `AudioContext.getOutputTimestamp()` every 20 ms and interpolate between
advancing output-position pairs that bracket the sample. Do not extrapolate.
Brackets wider than 100 ms, missing positions and closed-context gaps remain
unmapped. The browser supplies an
[estimated output position](https://webaudio.github.io/web-audio-api/#dom-audiocontext-getoutputtimestamp).

Every five seconds, make five no-store round trips to a measurement endpoint
using the capture host's `CLOCK_MONOTONIC`; select the minimum RTT. With browser
performance times `p0`, `p1` and server stamp `s`, all in milliseconds:

```text
offsetMS = s - (p0 + p1) / 2
offsetIntervalMS = [s - p1, s - p0]
latencyEstimateMS = interpolatedOutputPerformanceMS + offsetMS - sourceMS
```

Use the nearest calibration within ten seconds. Report an engineering allowance
of RTT/2, half the output bracket, nearby calibration drift and 1 ms for timer
granularity. This is not a formal accuracy bound: device timestamps, network
asymmetry and browser output estimates remain uncertain. Do not add
`baseLatency` or `outputLatency` to an estimate already mapped through
`getOutputTimestamp()`.

## Results and limits

The [recorded Chromium run](validation/audio-2026-11-17.json) mapped all 240,541
real quanta across 644.887 seconds of output and 641.419 seconds of PCM. Maximum
estimated latency was 102.941 ms, or 115.587 ms with its paired allowance; no
estimate exceeded 200 ms. There were 605 underrun quanta and 76,167 underrun
sample frames. See [hardware validation](hardware-validation.md#browser-audio-timing).

Dropped audio has no output latency and must not count as fast playback. Report
unmapped quanta, clock discontinuities and gaps alongside latency. Samples
already handed to the browser output path cannot be recalled by the application;
a diagnostic worklet pause exposed delayed or ambiguously mapped output there.

Keep transport stalls, server-writer delays, real ALSA overruns and physical USB
reconnects separate: each probes a different recovery path. Neither this method
nor a successful recovery establishes a universal device or kernel-call bound.
