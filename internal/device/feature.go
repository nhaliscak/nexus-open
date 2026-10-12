package device

import (
	"fmt"
	"strings"
)

// HID feature reports understood by the Nexus firmware.
//
// Report 3 is a command channel: byte 0 is the report ID, byte 1 the command,
// and any remaining bytes are arguments. Report 5 is read-only and carries the
// firmware version string. Command values are cross-referenced from
// willneedit/NexusTool and jurkovic-nikola/OpenLinkHub, which both drive the
// device successfully with them.
const (
	reportIDCommand  = 0x03
	reportIDFirmware = 0x05

	cmdSetBrightness = 0x01
	cmdBlankScreen   = 0x04
	cmdPlayAnimation = 0x0d
	cmdStopAnimation = 0x0f

	// firmwareReportLen is the buffer size requested for report 5. The device
	// returns at most this many bytes; the version string starts at offset 6.
	firmwareReportLen     = 64
	firmwareVersionOffset = 6

	// MinAnimation and MaxAnimation bound the built-in firmware animations.
	MinAnimation = 1
	MaxAnimation = 3

	// hardwareModeAnimation is the animation the firmware plays at power-on.
	hardwareModeAnimation  = 1
	hardwareModeBrightness = 100
)

func brightnessReport(value int) ([]byte, error) {
	if value < 0 || value > 100 {
		return nil, fmt.Errorf("brightness must be 0-100, got %d", value)
	}
	return []byte{reportIDCommand, cmdSetBrightness, byte(value)}, nil
}

func playAnimationReport(animation int, loop bool) ([]byte, error) {
	if animation < MinAnimation || animation > MaxAnimation {
		return nil, fmt.Errorf("animation must be %d-%d, got %d", MinAnimation, MaxAnimation, animation)
	}
	var l byte
	if loop {
		l = 1
	}
	return []byte{reportIDCommand, cmdPlayAnimation, byte(animation), l}, nil
}

func stopAnimationReport() []byte { return []byte{reportIDCommand, cmdStopAnimation} }

func blankScreenReport() []byte { return []byte{reportIDCommand, cmdBlankScreen} }

// hardwareModeReports returns the command sequence that hands the display
// back to the firmware: loop the power-on animation at full brightness. This
// is what OpenLinkHub sends on shutdown; without it the panel keeps showing
// the last frame the host sent.
func hardwareModeReports() [][]byte {
	anim, _ := playAnimationReport(hardwareModeAnimation, true)
	bright, _ := brightnessReport(hardwareModeBrightness)
	return [][]byte{anim, bright}
}

// parseFirmwareVersion extracts the NUL-terminated ASCII version string from
// a report 5 response. buf includes the leading report ID byte.
func parseFirmwareVersion(buf []byte) (string, error) {
	if len(buf) <= firmwareVersionOffset {
		return "", fmt.Errorf("firmware report too short: %d bytes", len(buf))
	}
	if buf[0] != reportIDFirmware {
		return "", fmt.Errorf("unexpected firmware report ID 0x%02x", buf[0])
	}
	raw := buf[firmwareVersionOffset:]
	if i := strings.IndexByte(string(raw), 0); i >= 0 {
		raw = raw[:i]
	}
	for _, c := range raw {
		if c < 0x20 || c > 0x7e {
			return "", fmt.Errorf("firmware version contains non-printable byte 0x%02x", c)
		}
	}
	// Firmware 2.2.6.0 pads the string with a trailing space.
	version := strings.TrimSpace(string(raw))
	if version == "" {
		return "", fmt.Errorf("firmware version is empty")
	}
	return version, nil
}
