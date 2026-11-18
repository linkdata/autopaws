# Browser audio measurement

This is the issue 43 lab procedure for the Linux KVM host. It measures an ALSA
capture-timestamp estimate to the browser's estimated device-output position;
it is not an acoustic loopback measurement. A queue-capacity test alone does
not establish the 200 ms end-to-end target. See the current result and remaining
checks in [Hardware validation](hardware-validation.md#release-validation-draft-2026-11-17).

## Reproduce a run

Use the same host kernel for the daemon and the QA proxy. The browser may be
local or reach the proxy through a localhost SSH forward. Record the build ID,
source hashes, OS/kernel, devices and USB paths, browser version, target viewport,
requested video size, and negotiated PCM rate/channels/period/kernel-buffer size.
Stop competing PCM owners, or apply the per-card policy described in
[the ALSA notes](../alsa/README.md); record which was necessary.

The current lab harness is `/tmp/autopaws-audio-qa`. It is separate from the
product and contains `main.go`, `go.mod`, `go.sum`, `instrument.py`, `run.sh`,
`collector.mjs`, `summary.py`, the Go/Node tests, and `METHOD.md`. Exact harness
sources, production copies and raw metadata are retained in the local evidence
archive `autopaws-audio-validation-2026-11-17.tar.gz` (626,324 bytes, 62 files),
SHA256 `0b89ebad5daeb51ef70aa6108ebdf6c235517d8343f223f7734eb5cc4bb93717`.
Use that matching archive to reproduce these commands. Its lab scripts retain
this setup's checkout and working-directory paths; adjust those paths to the
matching source checkout when reproducing elsewhere, then verify the module
hashes. The harness is not a product command or a committed repository tool.
The repository retains [compact result JSON](validation/audio-2026-11-17.json),
not the full raw stream.

1. Build the final daemon and start it with the selected serial/video devices,
   target coordinates and `-mouse-button-mode relative` for this Linux target.
   Enable the API and web on loopback, audio auto-selection, and the intended
   `web-fps` (20 in this setup). Record the actual bound addresses. If token
   authentication is enabled, use the matching secret and record that fact
   without including the secret in evidence.
2. On that same Linux host, start the lab proxy with a new output filename:

   ```sh
   cd /tmp/autopaws-audio-qa
   bash run.sh -backend http://127.0.0.1:WEB_PORT -listen 127.0.0.1:0 \
     -output /tmp/autopaws-audio-qa/RUN.json
   ```

   Replace `WEB_PORT` with the daemon port and `RUN` with a new run name.
   `run.sh` copies the current three audio/helper modules and adds observation
   code, then builds the proxy. Other assets and the media WebSocket use the
   actual daemon. Freeze production assets during the run. Save
   `source-hashes.json` and `instrumentation.diff` with the result; review the
   diff to confirm that queue, drop, rendering, reset and ACK decisions remain
   unchanged. The observer adds CPU and message load.
3. Open the printed localhost URL in the browser being tested. Use the real
   Audio button gesture, keep the page visible and the output device running,
   and enable view-only mode unless input is the subject of that check. Verify
   video, mute/unmute, negotiated rate/channels and advancing mapped output
   observations. Missing/nonadvancing output positions are unavailable
   measurement, not zero latency.
4. Collect at least 600 seconds between first and last mapped output observations
   and at least 600 seconds of real PCM, allowing additional time for startup,
   reporting and intentional reconnect gaps. Record both durations and gaps
   separately. Around the middle of the run,
   request a bounded transport delivery stall:

   ```sh
   curl --fail --request POST http://127.0.0.1:PROXY_PORT/qa/stall
   ```

   This proxy pauses forwarding for about two seconds, closes both WebSocket
   legs and discards that transport backlog. Verify automatic reconnect,
   preserved audio choice and recent audio after recovery. It is not proof of a
   two-second blocked daemon writer: fake-source Go tests separately exercise
   the server writer deadline and queue discard.
5. After both duration thresholds are satisfied, leave the context and observer
   alive for about three more seconds. The observer already batches once per
   second; no forced page-script call is needed. Save the last completed report
   before stopping the test, then summarize:

   ```sh
   python3 summary.py /tmp/autopaws-audio-qa/RUN.json > /tmp/autopaws-audio-qa/RUN-summary.json
   ```

   Archive the raw report stream, merged summary, proxy events, exact harness
   and production sources/hashes, and instrumentation diff before another run
   regenerates the working copies. Store the archive's SHA256 in the compact
   repository result JSON. Stop only the owned proxy/browser/daemon used for
   the test.

Physical USB unplug/replug is a separate check. The owner must identify the
capture or serial device before unplugging it; a fake path or software pause is
not an unplug test. Do not replace that result with a follow-up or infer it from
unit tests.

## Timing and counters

The instrumented queue observes the first source sample actually copied into
each output quantum, its output-frame index, sample counts, and queue sizes.
Quiet PCM is still real source data; nonzero amplitude is not the criterion.
Only metadata is saved, with no PCM, screenshots or typed input.

For the observed sample:

```text
sourceMS = sourceTimestampNS / 1e6 + relativeCopiedSampleMS
contextSeconds = quantumCurrentTime + firstOutputFrameIndex / sampleRate
```

The observer samples `AudioContext.getOutputTimestamp()` every 20 ms. It waits
for advancing output-position pairs to bracket that sample, then interpolates
its performance-clock output time. It does not extrapolate future output.
Brackets wider than 100 ms, missing positions and context-close gaps remain
unmapped. The browser API describes an estimated output position; see the
[Web Audio definition](https://webaudio.github.io/web-audio-api/#dom-audiocontext-getoutputtimestamp).

Every five seconds, five no-store `/qa/clock` round trips sample the proxy's
server `CLOCK_MONOTONIC`; use the minimum-RTT observation. Given local browser
performance times `p0`, `p1` and the server stamp `s` in milliseconds:

```text
offsetMS = s - (p0 + p1) / 2
offsetIntervalMS = [s - p1, s - p0]
latencyEstimateMS = interpolatedOutputPerformanceMS + offsetMS - sourceMS
```

Choose the nearest calibration at most ten seconds away. Report an engineering
allowance of RTT/2 + half the output-position bracket + nearby calibration drift
+ 1 ms for timer/rounding granularity. This is not a formal accuracy bound:
ALSA/device timestamp estimation, asymmetrical network delay and browser output
position error remain limitations. Do not add `baseLatency`/`outputLatency` again
to a result already mapped through `getOutputTimestamp()`; retain them as context
diagnostics.

Record mapped/unmapped quanta, real/underrun frames, observed output span, queue
maximum, negative/out-of-range estimates, p95/max estimates, max estimate plus
allowance, counts above 200 ms, report errors and skipped reports. Dropped audio
has no output latency and is not recorded as fast playback. Statistics cover the
first real sample per quantum, not every sample in a discontinuous block. Metadata
is batched once per second; this batching does not delay playback. `summary.py`
merges diagnostic deltas across the whole file; the last raw report alone omits
earlier outlier detail.

A completed result must name gaps, errors, missing mapping and source/output
accuracy limits, as well as the latency distribution. Run 4's measured estimates
and paired allowance were below the target, as recorded below; the remaining
browser and physical recovery matrix is still incomplete.

## Distinguish the recovery probes

| Probe | What it establishes | What it does not establish |
|---|---|---|
| Injected production video-write delay | Actual server writer waits, replaces its old audio subscription after a slow JPEG, and resumes browser playback | A blocked kernel syscall or physical unplug |
| Proxy transport stall/reconnect | Bounded delivery interruption, discarded transport backlog, viewer reconnect and preserved choice | A blocked server writer or unplugged USB device |
| Owned daemon reader pause beyond the PCM buffer | Real ALSA overrun, PREPARE, new audio generation and same-format graph recovery | Physical unplug or immunity to all kernel stalls |
| Instrumented worklet pause | Audio/DSP timeline freeze and application expiry on resumed processing | A universal browser/output-device latency bound |

A diagnostic-only worklet pause is available as
`POST /qa/pause-worklet?ms=500`. The copied worklet records Date/performance/context
clocks before and after a finite one-shot busy loop. It adds no normal PCM ACK.
Check the actual recorded duration/outcome; scheduling and clock behavior prevent
a hard realtime promise. Use a fresh ordinary timing run after this diagnostic,
and retain both results.

## Failures, fixes and output limitations

Run 2 failed on an earlier implementation: eight quanta exceeded 200 ms, the
maximum estimate was 555.512 ms, and 14 quanta were unmapped. An approximately
490 ms audio-context versus elapsed-time pause was observed. Exact old output
brackets were not retained, so its physical mechanism and whether those outliers
were queue replay are unproved. It exposed an aging risk in the use of
`AudioContext.currentTime`, validated separately by synthetic pause tests.
Production now uses a bracketed page performance/wall-clock pair and a worklet
elapsed-time clock. The Date-based
fallback advances during an audio timeline freeze; a native shared monotonic
epoch is preferred when available. Rollback flushes PCM while preserving the
source/page timing reference. Same-WebSocket generation resets also retain that
reference, while clearing PCM/sequence state. Synthetic tests verify rejection of
delayed new-sequence and first-new-generation audio. The first-ever reference or
a new connection/context cannot independently reveal absolute prior transport
age, and a coincident wall adjustment can still affect the fallback.

Run 3 deliberately paused the worklet for 500 ms. Six quanta were estimated at
about 553 ms and 11 earlier quanta were unmapped. The six outlier quanta were
rendered at context times 47.696–47.709333 seconds, before the pause began at
47.712 seconds. No stale post-pause rendering was observed. Thus the fixture
found audio already handed to the browser output path delayed or ambiguously
mapped across the output-clock discontinuity; the application queue cannot recall
that audio. This diagnostic is not a latency pass and must remain in the record.

The ordinary final-code run 4 completed with 644.887 seconds of mapped output
span and 641.419 seconds of real 48 kHz stereo PCM. All 240,541 real quanta were
mapped; none had a negative estimate or exceeded 200 ms, including its paired
engineering allowance. Maximum estimate was 102.941 ms, maximum paired upper
estimate 115.587 ms, and the 95th-percentile upper 1 ms histogram bin was 87 ms.
The worklet ring peaked at 49.875 ms. Report errors/skipped reports were zero;
605 underrun quanta and 76,167 underrun sample frames remain in the result.
The run included a 0.400324-second real ALSA-reader pause and a roughly two-second
transport pause/reconnect, retaining the audio choice. Its final completed report
and exact sources are archived; full-precision statistics and context history
are in [the compact result](validation/audio-2026-11-17.json).

This establishes the observed target result for this configuration and method,
not an acoustic guarantee or completed release acceptance. The [current hardware matrix](hardware-validation.md#release-validation-draft-2026-11-17)
records the independent Firefox and physical USB recovery results. Historical failures and the diagnostic output-pause
limitation remain part of the record; do not exclude them from the conclusion.

## Native server-writer recovery probe

A separate 75.965-second Chromium probe used real HDMI video/ALSA capture and
the production server and browser modules. A loopback-only fixture wrapped one
`net.Conn.Write`, identifying a kind-1 64-byte media header and JPEG SOI before
inserting a 200 ms userspace wait. The observed delay was 201.052 ms, and the
complete write took 201.073 ms. This exercises the production writer's elapsed
time/purge path; it does not simulate a blocked kernel call.

The PCM device stayed open once. Audio subscriptions changed from one open/zero
closed to two open/one closed, with one active in the before/after observations: the old
100 ms queue was replaced. The same browser context/node and audio generation
remained active, Audio stayed enabled and no console error occurred. The probe
recorded 28,421 mapped real quanta, no unmapped or above-200-ms estimate, maximum
91.637 ms (104.297 ms with paired allowance), and 9,969 underrun sample frames.
After closing the viewer and joining the fixture, both subscriptions and the one
PCM owner were closed. This short recovery probe supplements the completed
ten-minute run rather than replacing it.

The exact writer fixture, observation copies/raw metadata and synthetic native
DHT/insecure-context fixture are retained in the local supplementary archive
`autopaws-browser-recovery-validation-2026-11-17.tar.gz`, SHA256
`d173e80ad481a2fbc5891848aefd3911c36653c8cfdc0407e1d788d2ffdf1d0c`
(71,298 bytes, 36 files). It contains no PCM, target screenshots, credentials or
binaries. The compact repository JSON records its identity and results.
