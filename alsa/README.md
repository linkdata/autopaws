# Raw ALSA capture

`Open(ctx, path)` configures one capture PCM and starts one fixed worker. Startup
uses `O_NONBLOCK` to detect a busy substream, then clears it before blocking
`READI_FRAMES`. No PCM descriptor is registered with Go's runtime poller.

The source prefers exactly two channels, then one, with S16_LE interleaved
samples. It tests 48000 Hz through `HW_REFINE`; a containing range alone does not
prove support. Otherwise `HW_PARAMS` selects a supported point from the refined
space. Negotiated singleton values and allocation arithmetic are checked. Periods
span 20–100 ms and the buffer contains 4–8 periods. This is a requested kernel
buffer range, not an end-to-end latency guarantee.

`SW_PARAMS` enables `CLOCK_MONOTONIC` timestamps, one-period `avail_min`, start
threshold 1 and stop threshold equal to the buffer size. The first read starts
capture; the package never issues START. EPIPE/ESTRPIPE recovery uses PREPARE,
increments the source generation, clears every subscriber queue, and restarts
sequence numbering. A reopened source also gets a new process-unique generation.

Each block estimates the first sample's capture time as:

```text
status.tstamp - floor((status.delay + returned_sample_frames) * 1e9 / rate)
```

The timestamp's unit is nanoseconds in the server's `CLOCK_MONOTONIC` domain.
ALSA's capture delay includes unread captured frames and the driver's additional
delay. DMA position granularity and delay reporting limit the estimate's accuracy;
it is not a guarantee about analog acquisition time. No timestamp is substituted
from read completion. See the kernel's [timestamp documentation](https://www.kernel.org/doc/html/latest/sound/designs/timestamping.html)
and [capture status implementation](https://github.com/torvalds/linux/blob/v7.0/sound/core/pcm_native.c#L953-L1030).

Each subscription holds at most 100 ms by sample-frame count, drops oldest blocks
on overflow and discards blocks whose end-of-capture time is older than 100 ms
before returning them. End-of-capture is the first-sample timestamp plus the
block's sample-frame duration; the subtraction avoids counting acquisition of
that block as application queue residence. A newly completed 100 ms block is
therefore usable. Each receiver owns its bytes. This leaves 100 ms of the planned
200 ms application-queue ceiling for the browser worklet. A writer must fetch
immediately before transmission and discard any block it held across a stall.
The queue ceiling excludes kernel buffers, TCP/WebSocket buffering and output
device latency. There is no PulseAudio/PipeWire fallback.

Close marks stop before DROP, wakes subscription waiters, and joins the worker
that closes the descriptor. DROP never waits on a mutex held by blocking READI.
An already-known terminal read failure wakes existing and new subscribers before
driver close finishes; `Close` still joins that close and preserves its error.
PREPARE, STATUS and final close are serialized with DROP so a descriptor is not
closed or reused during an executing ioctl. Kernel calls can outlast a context;
the package retains ownership and does not abandon a blocked worker. Hardware
validation of DROP wakeup, busy-open timing, actual timestamps and recovery is
separate from fake-kernel tests.

Dedicated hardware tests are opt-in and never auto-select a microphone or card:

```sh
AUTOPAWS_ALSA_TEST_DEVICE=/dev/snd/pcmC1D0c go test -v ./alsa -run '^TestHardware'
```

Choose the intended exclusive capture substream. The tests capture sample blocks,
measure a second busy open and shutdown, and deliberately stall one read beyond
the kernel buffer duration to exercise real overrun recovery. They do not save
PCM bytes. Without the explicit device variable they skip, including on CI.
