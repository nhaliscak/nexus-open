package device

import (
	"bytes"
	"strings"
	"testing"
)

func TestBrightnessReport(t *testing.T) {
	got, err := brightnessReport(42)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{0x03, 0x01, 42}; !bytes.Equal(got, want) {
		t.Errorf("got % x, want % x", got, want)
	}
	for _, v := range []int{-1, 101} {
		if _, err := brightnessReport(v); err == nil {
			t.Errorf("brightness %d: expected error", v)
		}
	}
}

func TestPlayAnimationReport(t *testing.T) {
	tests := []struct {
		anim int
		loop bool
		want []byte
	}{
		{1, true, []byte{0x03, 0x0d, 0x01, 0x01}},
		{3, false, []byte{0x03, 0x0d, 0x03, 0x00}},
	}
	for _, tt := range tests {
		got, err := playAnimationReport(tt.anim, tt.loop)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, tt.want) {
			t.Errorf("anim %d loop %v: got % x, want % x", tt.anim, tt.loop, got, tt.want)
		}
	}
	for _, a := range []int{0, 4} {
		if _, err := playAnimationReport(a, false); err == nil {
			t.Errorf("animation %d: expected error", a)
		}
	}
}

func TestSimpleCommandReports(t *testing.T) {
	if got := stopAnimationReport(); !bytes.Equal(got, []byte{0x03, 0x0f}) {
		t.Errorf("stop: got % x", got)
	}
	if got := blankScreenReport(); !bytes.Equal(got, []byte{0x03, 0x04}) {
		t.Errorf("blank: got % x", got)
	}
}

// The restore sequence must match what OpenLinkHub sends on shutdown.
func TestHardwareModeReports(t *testing.T) {
	got := hardwareModeReports()
	want := [][]byte{{0x03, 0x0d, 0x01, 0x01}, {0x03, 0x01, 0x64}}
	if len(got) != len(want) {
		t.Fatalf("got %d reports, want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("report %d: got % x, want % x", i, got[i], want[i])
		}
	}
}

func firmwareBuf(version string, size int) []byte {
	buf := make([]byte, size)
	buf[0] = reportIDFirmware
	copy(buf[firmwareVersionOffset:], version)
	return buf
}

func TestParseFirmwareVersion(t *testing.T) {
	got, err := parseFirmwareVersion(firmwareBuf("1.0.0.12", 64))
	if err != nil {
		t.Fatal(err)
	}
	if got != "1.0.0.12" {
		t.Errorf("got %q", got)
	}

	// Real device (firmware 2.2.6.0) pads with a trailing space.
	got, err = parseFirmwareVersion(firmwareBuf("2.2.6.0 ", 32))
	if err != nil || got != "2.2.6.0" {
		t.Errorf("padded: got %q, %v", got, err)
	}

	// No NUL terminator: the string runs to the end of the buffer.
	got, err = parseFirmwareVersion(firmwareBuf("2.1", firmwareVersionOffset+3))
	if err != nil || got != "2.1" {
		t.Errorf("unterminated: got %q, %v", got, err)
	}
}

func TestParseFirmwareVersion_Errors(t *testing.T) {
	wrongID := firmwareBuf("1.0", 64)
	wrongID[0] = 0x03
	nonPrintable := firmwareBuf("1\x01", 64)

	tests := map[string][]byte{
		"too short":     {0x05, 0, 0},
		"wrong id":      wrongID,
		"empty":         firmwareBuf("", 64),
		"blank":         firmwareBuf("   ", 64),
		"non-printable": nonPrintable,
	}
	for name, buf := range tests {
		if _, err := parseFirmwareVersion(buf); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestNexusDevice_FeatureCommandsRequireConnection(t *testing.T) {
	n := &NexusDevice{logger: discardLogger(), stopReconnect: make(chan struct{})}

	calls := map[string]func() error{
		"SetBrightness":       func() error { return n.SetBrightness(50) },
		"PlayAnimation":       func() error { return n.PlayAnimation(1, true) },
		"StopAnimation":       n.StopAnimation,
		"BlankScreen":         n.BlankScreen,
		"RestoreHardwareMode": n.RestoreHardwareMode,
		"GetFirmwareVersion":  func() error { _, err := n.GetFirmwareVersion(); return err },
	}
	for name, call := range calls {
		err := call()
		if err == nil || !strings.Contains(err.Error(), ErrDeviceDisconnected.Error()) {
			t.Errorf("%s: got %v, want ErrDeviceDisconnected", name, err)
		}
	}
}

func TestNexusDevice_FeatureCommandsOnClosedHandle(t *testing.T) {
	n := &NexusDevice{logger: discardLogger(), stopReconnect: make(chan struct{})}
	n.handle = &usbHandle{closed: true}
	n.connected = true

	if err := n.RestoreHardwareMode(); err == nil || !strings.Contains(err.Error(), "handle closed") {
		t.Errorf("RestoreHardwareMode: got %v, want handle closed error", err)
	}
	if _, err := n.GetFirmwareVersion(); err == nil || !strings.Contains(err.Error(), "handle closed") {
		t.Errorf("GetFirmwareVersion: got %v, want handle closed error", err)
	}
}

func TestNexusDevice_ArgumentValidation(t *testing.T) {
	n := &NexusDevice{logger: discardLogger(), stopReconnect: make(chan struct{})}
	if err := n.SetBrightness(101); err == nil || strings.Contains(err.Error(), ErrDeviceDisconnected.Error()) {
		t.Errorf("SetBrightness(101): got %v, want range error", err)
	}
	if err := n.PlayAnimation(9, false); err == nil || strings.Contains(err.Error(), ErrDeviceDisconnected.Error()) {
		t.Errorf("PlayAnimation(9): got %v, want range error", err)
	}
}
