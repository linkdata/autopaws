# Raw ALSA capture

`Open(ctx, path)` opens one capture PCM exclusively. It prefers 48 kHz stereo
S16_LE interleaved samples, falling back to supported mono/rate settings. Requested
periods span 20–100 ms with 4–8 periods per kernel buffer; these are not
end-to-end latency limits.

Overrun or suspend recovery advances the source generation, clears subscriber
queues and restarts sequence numbering. Repeated recovery without valid samples
ends capture with a terminal error. Reopening a source also gives it a new
process-unique generation.

After reconnecting a device, retry terminal audio by re-listing capture nodes
and calling `set_capture_device` with the current video path. Reselecting the
same path is accepted. Automatic audio resolves the selected video's USB ancestor
to its current PCM. Closing every viewer and opening a new one also retries
audio. Package callers must open a new source after terminal failure.

Each block estimates the first sample's capture time as:

```text
status.tstamp - floor((status.delay + returned_sample_frames) * 1e9 / rate)
```

The timestamp's unit is nanoseconds in the server's `CLOCK_MONOTONIC` domain.
ALSA's capture delay includes unread frames and driver delay. DMA granularity and
delay reporting limit accuracy; this is not an analog acquisition guarantee.
See the kernel's [timestamp documentation](https://www.kernel.org/doc/html/latest/sound/designs/timestamping.html).

Each subscription holds at most 100 ms by sample-frame count, drops oldest blocks
on overflow and discards blocks whose end-of-capture time is older than 100 ms
before returning them. End-of-capture is the first-sample timestamp plus the
block duration. Each receiver owns its bytes. Fetch blocks immediately before
transmission and discard any held across a stall. Queue limits exclude kernel
buffers, TCP/WebSocket buffering and output device latency.

`Close` wakes subscribers and joins the capture worker. Terminal failures notify
subscribers before driver close finishes. Kernel calls can outlast a context;
the package retains ownership until they return.

## Exclusive access with WirePlumber 0.5

If open reports `ErrBusy`, another process owns the capture PCM. On a desktop,
`wpctl status --name` lists PipeWire devices; inspect the capture card's numeric
ID with `wpctl inspect ID` to obtain its exact `device.name`. To reserve only
that card for autopaws, create
`~/.config/wireplumber/wireplumber.conf.d/51-autopaws-capture.conf`:

```conf
monitor.alsa.rules = [
  {
    matches = [ { device.name = "alsa_card.REPLACE_WITH_YOUR_CAPTURE_CARD_NAME" } ]
    actions = { update-props = { device.disabled = true } }
  }
]
```

Replace the example name with the complete observed name, then run
`systemctl --user restart wireplumber`. The selected card disappears from desktop
audio; its raw ALSA PCM remains available to autopaws. Restore desktop access by
removing this fragment and restarting WirePlumber again.

This is the WirePlumber 0.5 configuration format; see the official
[ALSA rules and device properties](https://pipewire.pages.freedesktop.org/wireplumber/daemon/configuration/alsa.html)
and [configuration fragments](https://pipewire.pages.freedesktop.org/wireplumber/daemon/configuration/modifying_configuration.html).
Use the format for your installed WirePlumber version. There is no
PulseAudio/PipeWire fallback.
