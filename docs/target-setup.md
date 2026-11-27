# Target setup

These settings concern the controlled computer, independently of the host OS.
Set `target-layout` to the target's keyboard layout. Set `screen-width` and
`screen-height` to its coordinate dimensions; capture resolution can differ.

## Display setup

Use the highest resolution where HDMI output and the actual capture frame
match pixel for pixel. A card accepting 4K but capturing 1080p loses detail.
If capture resolution is wrong on a Linux host, inspect formats with
`autopaws-kvm --list-devices`. On all hosts, check negotiated dimensions with API
`get_device_info` and a fresh frame. Desktop
coordinate units can differ from output pixels when display scaling is enabled.

Start with a single or mirrored display. For an extended desktop, make the
captured display primary/main and place it at the top-left of the arrangement,
with other displays to its right or below. The tested macOS target reached only
the primary display; the tested GNOME Wayland target addressed the whole virtual
desktop and needed display mapping.

- **macOS:** System Settings → Displays → select the captured display → Use as → Main display, or mirror.
- **Windows:** Settings → System → Display → select the captured display → Make this my main display, or duplicate.
- **GNOME:** Settings → Displays → Mirror, or configure the extended desktop with [display mapping](#display-mapping).

When mirroring, optimize output for the capture display to avoid scaling or
letterboxing from another panel. Keep text readable at the chosen size.

Before positioned clicks or drags, capture a fresh frame and move the pointer
to two widely separated image points. Check the actual target cursor at both.
If it stays on another display or moves by the wrong amount, correct the display
setup or mapping. Recheck after layout, resolution or scaling changes.
autopaws does not detect display arrangements. Relative movement across displays
does not establish the absolute HID range. Selecting a capture device does not
redirect HID input; geometry cannot reach
a display outside the target's absolute HID range.

## Display mapping

Default `legacy` mode uses identity mapping, suitable for single/mirrored
displays and targets whose absolute input reaches only the captured display.
If input spans a larger desktop, open the viewer's **Pointer setup**, select
extended mapping, enter the captured display's signed desktop offset and native
size plus the full absolute input span, then click **Apply**.

For example, a 1920×1080 captured display at `(1920,0)` within an absolute input
span of `(0,0,3840,1200)` uses this API/MCP `set_mouse_mapping` request:

```json
{
  "geometry": {
    "offset": {"x": 1920, "y": 0}, "width": 1920, "height": 1080,
    "span": {"x": 0, "y": 0, "width": 3840, "height": 1200}
  }
}
```

Use the same desktop coordinate system and units for every geometry value.
Geometry activates after bounds validation. Screenshot coordinates remain
local to the captured image; callers do not add offsets to mouse actions.
`get_mouse_mapping` reports shared geometry, transform and readiness. Browser,
API and MCP use the same daemon state.

**Reset** / `reset_mouse_mapping` clears geometry while retaining the mode.
Select legacy and **Apply**, or send `{"mode":"legacy"}` to `set_mouse_mapping`,
to restore identity mapping. Start with `-mouse-mode extended` to block positioned
input until geometry is configured.

Mapping lasts for the daemon's current run and is not saved to YAML. Capture
context changes, including recovery, invalidate extended mapping. Reapply
geometry and take a fresh MCP screenshot afterward.

## Mouse compatibility

Use absolute coordinates from the latest screenshot for positioned movement,
clicks and drags. `button_mode` chooses the button report, not coordinate units.
Relative deltas are unscaled HID counts, so acceleration can make them differ
from screenshot pixels.

Windows and Linux normally use the default relative button reports. On macOS,
first call `mouse_move` to reach the click point or drag start, then test
positioned `mouse_click` and `mouse_drag` with `button_mode: "absolute"`. Verify
real clicks and sustained drag-and-drop in the target application. API success
alone does not establish a maintained hold.

If an absolute destination is ignored after relative or physical movement,
restart KVM with `-mouse-absolute-refresh` (YAML: `mouse-absolute-refresh: true`).
It defaults to `false`. When enabled, an unheld move passes through a neighboring
HID coordinate before the exact destination; new positioned presses also position
automatically. `get_device_info` reports `config.mouse_absolute_refresh`.
Refresh does not fix display geometry or adapter button compatibility.

Some CH9329 firmware combines relative and absolute mice in one HID interface.
On the tested macOS target, relative button presses did not register on this layout, and it
released an absolute hold when coordinates changed. Positioned absolute clicks
worked, but sustained absolute drags did not.

### Optional adapter USB mode

USB work mode 2 exposed separate mouse interfaces on one tested adapter and
passed screenshot-coordinate clicks and held text-selection drags in both button
modes. Firmware support varies; this is an optional compatibility setting.

1. Call API `get_adapter_usb_mode` and save the exact `raw_mode`. For example,
   `128` means hardware-pin-selected mode 0; restoring `0` would change the selection source.
2. Call `set_adapter_usb_mode` with `{"raw_mode":2}`. This persistent operation
   preserves other settings, including serial communication protocol mode 0
   and baud rate (normally 9600). USB work mode and serial communication mode differ.
3. After every setter attempt, reapply extended geometry if needed, capture a
   fresh frame and check alignment, clicks and sustained holds. Restore the saved
   raw value through the same setter when needed.

The [API methods](../api/PROTOCOL.md#adapter-usb-mode) require backend support
and verified replies. The setter verifies reset/re-enumeration and readback when
a change is needed, with best-effort rollback on failure. Use a supported vendor
utility if probing fails. Startup and MCP `setup` never change adapter settings.
A second adapter did not acknowledge reset, so its mode-2 behavior is unverified.
See the [compatibility record](validation/macos-mouse-compatibility-2026-11-27.json)
for firmware, descriptors and test limits.
