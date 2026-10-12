package device

import (
	"context"
	"sync"
	"time"

	"github.com/mantonx/nexus-open/internal/touch"
)

// MockDevice is a mock implementation of Device for testing.
type MockDevice struct {
	mu sync.RWMutex

	connected      bool
	shouldFailNext string // Which operation should fail next
	framesSent     int
	lastFrame      []byte
	touchEvents    []touch.Event
	featureCmds    []string // feature commands received, in order

	// Callbacks for testing
	OnConnect    func(ctx context.Context) error
	OnDisconnect func() error
	OnSendFrame  func(ctx context.Context, data []byte) error
}

// NewMockDevice creates a new mock device.
func NewMockDevice() *MockDevice {
	return &MockDevice{
		connected:   false,
		touchEvents: make([]touch.Event, 0),
	}
}

// Connect simulates connecting to a device.
func (m *MockDevice) Connect(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.OnConnect != nil {
		return m.OnConnect(ctx)
	}

	if m.shouldFailNext == "connect" {
		m.shouldFailNext = ""
		return ErrConnectionFailed
	}

	m.connected = true
	return nil
}

// Disconnect simulates disconnecting from a device.
func (m *MockDevice) Disconnect() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.OnDisconnect != nil {
		return m.OnDisconnect()
	}

	m.connected = false
	return nil
}

// IsConnected returns the mock connection status.
func (m *MockDevice) IsConnected() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.connected
}

// SendFrame simulates sending a frame to the device.
func (m *MockDevice) SendFrame(ctx context.Context, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.OnSendFrame != nil {
		return m.OnSendFrame(ctx, data)
	}

	if !m.connected {
		return ErrDeviceDisconnected
	}

	if m.shouldFailNext == "send_frame" {
		m.shouldFailNext = ""
		return ErrSendFailed
	}

	if len(data) != FrameSize {
		return ErrInvalidFrame
	}

	m.lastFrame = make([]byte, len(data))
	copy(m.lastFrame, data)
	m.framesSent++

	return nil
}

// ReadTouch returns mock touch events.
func (m *MockDevice) ReadTouch(ctx context.Context) ([]touch.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.connected {
		return nil, ErrDeviceDisconnected
	}

	events := m.touchEvents
	m.touchEvents = make([]touch.Event, 0)
	return events, nil
}

// Health performs a mock health check.
func (m *MockDevice) Health() error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !m.connected {
		return ErrDeviceDisconnected
	}

	return nil
}

// Mock control methods for testing

// SimulateTouch adds a mock touch event.
func (m *MockDevice) SimulateTouch(button int, pressed bool, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.touchEvents = append(m.touchEvents, touch.Event{
		Button:   button,
		Pressed:  pressed,
		Duration: duration,
	})
}

// FailNext makes the next call to the specified operation fail.
func (m *MockDevice) FailNext(operation string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.shouldFailNext = operation
}

// GetFramesSent returns the number of frames sent.
func (m *MockDevice) GetFramesSent() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.framesSent
}

// GetLastFrame returns the last frame data sent.
func (m *MockDevice) GetLastFrame() []byte {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.lastFrame == nil {
		return nil
	}
	frame := make([]byte, len(m.lastFrame))
	copy(frame, m.lastFrame)
	return frame
}

// SimulateDisconnect simulates a device disconnection.
func (m *MockDevice) SimulateDisconnect() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = false
}

// SetBrightness simulates setting device brightness.
func (m *MockDevice) SetBrightness(brightness int) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !m.connected {
		return ErrDeviceDisconnected
	}

	return nil
}

// GetFirmwareVersion returns a mock firmware version.
func (m *MockDevice) GetFirmwareVersion() (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !m.connected {
		return "", ErrDeviceDisconnected
	}

	return "1.0.0-mock", nil
}

// PlayAnimation records a play-animation command.
func (m *MockDevice) PlayAnimation(animation int, loop bool) error {
	if _, err := playAnimationReport(animation, loop); err != nil {
		return err
	}
	return m.recordFeature("play_animation")
}

// StopAnimation records a stop-animation command.
func (m *MockDevice) StopAnimation() error { return m.recordFeature("stop_animation") }

// BlankScreen records a blank-screen command.
func (m *MockDevice) BlankScreen() error { return m.recordFeature("blank_screen") }

// RestoreHardwareMode records a restore-hardware-mode command.
func (m *MockDevice) RestoreHardwareMode() error { return m.recordFeature("restore_hardware_mode") }

// GetFeatureCommands returns the feature commands received so far.
func (m *MockDevice) GetFeatureCommands() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]string(nil), m.featureCmds...)
}

func (m *MockDevice) recordFeature(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.connected {
		return ErrDeviceDisconnected
	}
	m.featureCmds = append(m.featureCmds, name)
	return nil
}

// GetDeviceInfo returns mock device info.
func (m *MockDevice) GetDeviceInfo() DeviceInfo {
	return DeviceInfo{
		Manufacturer: "Corsair",
		Product:      "iCUE Nexus (mock)",
		VendorID:     0x1b1c,
		ProductID:    0x1b8e,
	}
}
