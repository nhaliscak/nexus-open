// Package device provides an abstraction layer for USB device communication.
// It defines interfaces for interacting with the Corsair iCUE Nexus display device.
package device

import (
	"context"
	"errors"
	"time"

	"github.com/mantonx/nexus-open/internal/touch"
)

// Device represents a Nexus display device with USB communication capabilities.
type Device interface {
	// Connect establishes a connection to the device.
	// Returns ErrDeviceNotFound if no device is found.
	Connect(ctx context.Context) error

	// Disconnect closes the device connection and releases resources.
	Disconnect() error

	// IsConnected returns the current connection status.
	IsConnected() bool

	// SendFrame sends a complete frame of image data to the device.
	// The data must be in the correct format (640x48 pixels, RGBA).
	// Returns ErrDeviceDisconnected if the device is not connected.
	SendFrame(ctx context.Context, data []byte) error

	// ReadTouch reads touch input events from the device (non-blocking).
	// Returns empty slice if no events are available.
	ReadTouch(ctx context.Context) ([]touch.Event, error)

	// Health performs a health check on the device connection.
	// Returns nil if healthy, error otherwise.
	Health() error

	// HID Feature Report methods (may not be supported by all devices)
	SetBrightness(brightness int) error
	GetFirmwareVersion() (string, error)
	GetDeviceInfo() DeviceInfo
	PlayAnimation(animation int, loop bool) error
	StopAnimation() error
	BlankScreen() error

	// RestoreHardwareMode hands the display back to the firmware's own
	// animation. Call before Disconnect on shutdown.
	RestoreHardwareMode() error
}

// ConnectionConfig holds configuration for device connection.
type ConnectionConfig struct {
	VendorID         uint16        // USB Vendor ID (0x1b1c for Corsair)
	ProductID        uint16        // USB Product ID (0x1b8e for iCUE Nexus)
	ReconnectRetries int           // Number of reconnection attempts
	ReconnectDelay   time.Duration // Delay between reconnection attempts
}

// Common errors returned by Device implementations.
var (
	ErrDeviceNotFound     = errors.New("device not found")
	ErrDeviceDisconnected = errors.New("device disconnected")
	ErrInvalidFrame       = errors.New("invalid frame data")
	ErrConnectionFailed   = errors.New("connection failed")
	ErrInterfaceFailed    = errors.New("failed to claim USB interface")
	ErrConfigFailed       = errors.New("failed to configure device")
	ErrSendFailed         = errors.New("failed to send data")
)

// FrameConstants defines the display dimensions and buffer sizes.
const (
	DisplayWidth  = 640
	DisplayHeight = 48
	FrameSize     = DisplayWidth * DisplayHeight * 4 // RGBA
)
