# USB Protocol: Corsair iCUE Nexus

Corsair publishes no Linux SDK and no protocol documentation for the iCUE Nexus
(VID `0x1b1c`, PID `0x1b8e`). This document is the result of reverse engineering
the protocol from usbmon captures: with the `usbmon` kernel module loaded,
Wireshark captures raw URBs on the device's bus. The device generates enough
traffic on plug-in to reveal the frame and touch protocols without needing a
reference implementation or disassembly.

The implementation is pure Go via Linux usbfs ioctls — no libusb, no CGo, no
shared libraries. The relevant source is `internal/device/`.

## Transport

Nexus Open uses the Linux `usbfs` kernel interface (`/dev/bus/usb`) directly
via `USBDEVFS_BULK` ioctls — no libusb, no HID library, no CGo. The device
is discovered by scanning `/sys/bus/usb/devices` for the matching
`idVendor`/`idProduct` sysfs attributes, then opening the corresponding
`/dev/bus/usb/BBB/DDD` node. Interface 0 is claimed with
`USBDEVFS_CLAIMINTERFACE`; if a kernel driver already holds it (e.g. `usbhid`)
the driver is detached first with `USBDEVFS_IOCTL` / `USBDEVFS_DISCONNECT`.

The device exposes two interrupt endpoints on interface 0:

| Endpoint | Direction | Use |
|---|---|---|
| `0x02` | OUT | Frame data |
| `0x81` | IN | Touch HID reports |

Both are driven by `USBDEVFS_BULK` ioctls. Despite the ioctl name, usbfs
routes interrupt-endpoint transfers correctly — it is the standard technique
for interrupt endpoints via usbfs.

After claiming the interface, Nexus Open primes EP `0x81` with a short
read (100 ms timeout, errors ignored). Empirically, this is required before
the device will accept writes on EP `0x02`.

## Frame Format

The display is 640×48 pixels, RGBA, 4 bytes per pixel:

```
FrameSize = 640 × 48 × 4 = 122,880 bytes
```

Frames are delivered in **1024-byte packets** over EP `0x02`. Each packet
has an 8-byte header followed by up to 1016 bytes of pixel data:

```
Offset  Size  Value      Meaning
──────  ────  ─────      ───────
0       1     0x02       Fixed — identifies this as a display write command
1       1     0x05       Fixed — "send image" sub-command
2       1     0x40       Fixed
3       1     0 or 1     End-of-frame flag: 1 on the last chunk, 0 otherwise
4       1     chunk & 0xFF    Chunk index, low byte
5       1     chunk >> 8      Chunk index, high byte (always 0 for this frame size)
6       1     payloadLen & 0xFF   Bytes of pixel data in this packet, low byte
7       1     payloadLen >> 8     Bytes of pixel data in this packet, high byte
8+      ≤1016 pixel data    BGR-swapped RGBA (see below)
```

A full frame requires `⌈122880 / 1016⌉ = 121` packets (chunks 0–120).
The final chunk carries the remaining `122880 - 120 × 1016 = 960` bytes.

### Pixel byte order

The host renders frames as RGBA. The device expects **BGR** with the alpha
channel preserved in position. Each 4-byte pixel is reordered on the way out:

```
RGBA source:  [R][G][B][A]
USB packet:   [B][G][R][A]
```

This swap happens in `sendFrameToHandle` (`internal/device/nexus.go`) as the
packet is assembled, with no intermediate buffer allocation.

## Brightness Control

Brightness is set via a **HID SET_REPORT** control transfer (no HID library
required — it is a single `USBDEVFS_CTRLTRANSFER` ioctl):

```
bmRequestType  0x21   Class | Interface | Host→Device
bRequest       0x09   SET_REPORT
wValue         0x0303  type=Feature (3<<8), report_id=3
wIndex         0       Interface 0
wLength        3
Data           [0x03, 0x01, brightness]
```

`brightness` is a raw byte in the range `0–100`. `0` turns the backlight off.

## Other Feature Reports

Report 3 is a general command channel: byte 0 is the report ID (`0x03`),
byte 1 the command, and the rest are arguments. All are sent with the same
SET_REPORT request as brightness, with `wLength` set to the report length.
These commands are taken from [NexusTool](https://github.com/willneedit/NexusTool)
and [OpenLinkHub](https://github.com/jurkovic-nikola/OpenLinkHub), which both
use them against the device; they are implemented in `internal/device/feature.go`.

| Bytes | Command |
|---|---|
| `03 01 nn` | Set brightness, `nn` = 0–100 |
| `03 04` | Blank the screen |
| `03 0d aa ll` | Play built-in animation `aa` (1–3); `ll` = 1 to loop, 0 for once |
| `03 0f` | Stop the built-in animation |

Other command values are unknown.

**Returning to hardware mode.** The firmware keeps showing the last frame the
host sent until told otherwise. Sending `03 0d 01 01` (loop animation 1) then
`03 01 64` (brightness 100) hands the panel back to the firmware's own
animation. Nexus Open does this on shutdown via `RestoreHardwareMode`, falling
back to a blank frame if the command fails.

**Firmware version.** Report 5 is read with GET_REPORT
(`bmRequestType 0xA1`, `bRequest 0x01`, `wValue 0x0305`, `wLength 64`). The
response starts with the report ID; bytes 6 onwards are a NUL-terminated
ASCII version string. Bytes 1–5 are unknown.

## Touch Input

Touch events arrive as 512-byte HID input reports on EP `0x81`
(wMaxPacketSize=512; the device fills only the first few bytes). The fields
Nexus Open reads:

```
Byte  Meaning
────  ───────
0     Report ID (0x01)
1     Report type (0x02)
2     Touch marker (0x21 when touch data is present)
3–4   Unknown — not read
5     Touch state: 0 = not touching, non-zero = finger down
6     X position, low byte  (raw range 0–486)
7     X position, high byte
```

The raw X coordinate ranges from 0 to 486 (observed device maximum) and is
normalized to screen pixels (0–639) by `HIDTouchReader`. The reader
auto-calibrates `rawMax` upward if it ever sees a higher value.

The report is read with a 200 ms timeout. A timeout returns `(0, nil)` so
the caller loops without error handling overhead. `ENODATA` from the ioctl
is treated identically to `ETIMEDOUT`.

`HIDTouchReader` (`internal/touch/reader.go`) debounces raw reports into
`touch.Event` values carrying a tap X position, swipe pixel delta, live swipe
progress, and final velocity. The gesture classifier distinguishes taps from
swipes using configurable distance and velocity thresholds.

## Reconnection

`NexusDevice.monitorConnection` runs in a background goroutine. It polls
`Health()` every second. On failure it:

1. Closes and nulls the stale handle under `mu`.
2. Waits a 2-second settle delay — empirically needed to let the device
   firmware reset its USB state before the interface can be claimed again.
   Skipping this causes rapid open/close cycles that require a physical
   replug to recover.
3. Calls `connectOnce()` (a single attempt, not the 3-attempt `Connect()`).
4. Backs off exponentially up to 30 seconds on repeated failures.

A separate `writeMu` mutex serialises concurrent frame and brightness writes
so a reconnect in progress cannot interleave with an in-flight frame transfer.

## Concurrency model

```
mu (RWMutex)      — guards connected, handle
writeMu (Mutex)   — serialises all EP 0x02 OUT and control transfers
```

`SendFrame` and `SetBrightness` take `mu` read-lock to snapshot the handle,
then acquire `writeMu` before any ioctl. `connectOnce` takes `mu` write-lock
when installing a new handle. `monitorConnection` takes `mu` write-lock only
to clear the handle before the settle delay, never while holding `writeMu`.
