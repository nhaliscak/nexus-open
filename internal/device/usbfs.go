// Pure-Go USB implementation using the Linux usbfs kernel interface
// (/dev/bus/usb). Replaces the cgo+libusb path with direct ioctls so the
// package builds with CGO_ENABLED=0 and has no runtime shared-library dep.
//
// Only the four operations the Nexus device needs are implemented:
//   - open by VID/PID (enumerate /sys, open /dev/bus/usb node)
//   - interrupt write  EP 0x02 OUT  (frame chunks)
//   - interrupt read   EP 0x81 IN   (touch events)
//   - control transfer              (HID SET_REPORT for brightness)


package device

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// usbfs ioctl numbers for amd64 Linux — derived from <linux/usbdevice_fs.h>
// using the _IO/_IOR/_IOW/_IOWR macro expansion.
const (
	ioctlControl          = 0xc0185500 // _IOWR('U',  0, ctrltransfer{24 bytes})
	ioctlBulk             = 0xc0185502 // _IOWR('U',  2, bulktransfer{24 bytes})
	ioctlClaimInterface   = 0x8004550f // _IOR ('U', 15, uint32)
	ioctlReleaseInterface = 0x80045510 // _IOR ('U', 16, uint32)
	ioctlUsbfsIoctl       = 0xc0105512 // _IOWR('U', 18, usbfsIoctl{16 bytes})
	ioctlDisconnect       = 0x00005516 // _IO  ('U', 22)  — sub-code for USBDEVFS_IOCTL
)

// bulkTransfer mirrors struct usbdevfs_bulktransfer (usbdevice_fs.h).
// ep, len, timeout are 32-bit; data is a 64-bit pointer.
type bulkTransfer struct {
	ep      uint32
	length  uint32
	timeout uint32
	_       uint32 // padding to align data pointer
	data    uintptr
}

// ctrlTransfer mirrors struct usbdevfs_ctrltransfer.
type ctrlTransfer struct {
	requestType uint8
	request     uint8
	value       uint16
	index       uint16
	length      uint16
	timeout     uint32
	_           uint32 // padding to align data pointer
	data        uintptr
}

// usbfsIoctl mirrors struct usbdevfs_ioctl used to send sub-ioctls
// (e.g. driver disconnect) through USBDEVFS_IOCTL.
type usbfsIoctl struct {
	ifno      int32
	ioctlCode int32
	data      uintptr
}

// usbHandle wraps an open usbfs file descriptor.
type usbHandle struct {
	mu     sync.Mutex
	fd     int
	closed bool
}

// sysfsUSBDevices is the path scanned by findDevice. Overridden in tests.
var sysfsUSBDevices = "/sys/bus/usb/devices"

// usbOpen finds the device by VID/PID via sysfs, then opens its usbfs node.
func usbOpen(vid, pid uint16) (*usbHandle, string, string, error) {
	busNum, devNum, mfr, prod, err := findDevice(vid, pid)
	if err != nil {
		return nil, "", "", err
	}

	path := fmt.Sprintf("/dev/bus/usb/%03d/%03d", busNum, devNum)
	fd, err := unix.Open(path, unix.O_RDWR, 0)
	if err != nil {
		return nil, "", "", classifyOpenError(fmt.Errorf("open %s: %w", path, err))
	}

	h := &usbHandle{fd: fd}

	iface := uint32(0)
	_, _, claimErrno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd),
		ioctlClaimInterface, uintptr(unsafe.Pointer(&iface)))
	if claimErrno == unix.EBUSY {
		// A kernel driver holds the interface; detach it and retry once.
		if detachErr := h.detachKernelDriver(0); detachErr != nil {
			_ = unix.Close(fd)
			return nil, "", "", classifyOpenError(fmt.Errorf("detach kernel driver: %w", detachErr))
		}
		time.Sleep(100 * time.Millisecond)
		_, _, claimErrno = unix.Syscall(unix.SYS_IOCTL, uintptr(fd),
			ioctlClaimInterface, uintptr(unsafe.Pointer(&iface)))
	}
	if claimErrno != 0 {
		_ = unix.Close(fd)
		return nil, "", "", classifyOpenError(fmt.Errorf("claim interface 0: %w", claimErrno))
	}

	// Prime EP 0x81 IN so the device accepts EP 0x02 OUT writes.
	// Ignore errors — a timeout here is expected if no touch event is pending.
	primeBuf := make([]byte, 512)
	_, _ = h.interruptTransfer(0x81, primeBuf, 100)

	return h, mfr, prod, nil
}

func (h *usbHandle) close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true

	iface := uint32(0)
	unix.Syscall(unix.SYS_IOCTL, uintptr(h.fd), //nolint:errcheck
		ioctlReleaseInterface, uintptr(unsafe.Pointer(&iface)))
	_ = unix.Close(h.fd)
}

func (h *usbHandle) isClosed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closed
}

// writeFrame sends a 1024-byte packet to EP 0x02 OUT.
func (h *usbHandle) writeFrame(pkt []byte) error {
	if len(pkt) != 1024 {
		return fmt.Errorf("frame packet must be 1024 bytes, got %d", len(pkt))
	}
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return fmt.Errorf("USB write: handle closed")
	}
	n, err := h.interruptTransfer(0x02, pkt, 1000)
	if err != nil {
		return fmt.Errorf("USB write: %w", err)
	}
	if n != len(pkt) {
		return fmt.Errorf("USB write: short write %d/%d", n, len(pkt))
	}
	return nil
}

// ReadTouch reads one touch packet from EP 0x81 IN.
// Returns (0, nil) on timeout so the caller can loop without error handling.
func (h *usbHandle) ReadTouch(buf []byte, _ uint) (int, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return 0, fmt.Errorf("USB read: handle closed")
	}
	h.mu.Unlock()

	n, err := h.interruptTransfer(0x81, buf, 200)
	if err != nil {
		if isTimeout(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("USB read: %w", err)
	}
	return n, nil
}

// setFeatureReport sends a HID SET_REPORT feature report. data[0] is the
// report ID, as with hidraw.
func (h *usbHandle) setFeatureReport(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("USB set feature: empty report")
	}
	if h.isClosed() {
		return fmt.Errorf("USB set feature: handle closed")
	}
	xfer := ctrlTransfer{
		requestType: 0x21, // LIBUSB_REQUEST_TYPE_CLASS | LIBUSB_RECIPIENT_INTERFACE | LIBUSB_ENDPOINT_OUT
		request:     0x09, // HID SET_REPORT
		value:       (3 << 8) | uint16(data[0]), // type=Feature(3), report_id
		index:       0,
		length:      uint16(len(data)),
		timeout:     1000,
		data:        uintptr(unsafe.Pointer(&data[0])),
	}
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(h.fd),
		ioctlControl, uintptr(unsafe.Pointer(&xfer)))
	if errno != 0 {
		return fmt.Errorf("USB set feature 0x%02x: %w", data[0], errno)
	}
	return nil
}

// getFeatureReport issues a HID GET_REPORT for a feature report and returns
// the number of bytes the device wrote into buf (report ID included).
func (h *usbHandle) getFeatureReport(reportID byte, buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, fmt.Errorf("USB get feature: empty buffer")
	}
	if h.isClosed() {
		return 0, fmt.Errorf("USB get feature: handle closed")
	}
	xfer := ctrlTransfer{
		requestType: 0xa1, // LIBUSB_REQUEST_TYPE_CLASS | LIBUSB_RECIPIENT_INTERFACE | LIBUSB_ENDPOINT_IN
		request:     0x01, // HID GET_REPORT
		value:       (3 << 8) | uint16(reportID), // type=Feature(3), report_id
		index:       0,
		length:      uint16(len(buf)),
		timeout:     1000,
		data:        uintptr(unsafe.Pointer(&buf[0])),
	}
	r, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(h.fd),
		ioctlControl, uintptr(unsafe.Pointer(&xfer)))
	if errno != 0 {
		return 0, fmt.Errorf("USB get feature 0x%02x: %w", reportID, errno)
	}
	return int(r), nil
}

// interruptTransfer executes a USBDEVFS_BULK ioctl (which handles interrupt
// endpoints correctly on usbfs) for the given endpoint and buffer.
func (h *usbHandle) interruptTransfer(ep uint8, buf []byte, timeoutMs uint32) (int, error) {
	xfer := bulkTransfer{
		ep:      uint32(ep),
		length:  uint32(len(buf)),
		timeout: timeoutMs,
		data:    uintptr(unsafe.Pointer(&buf[0])),
	}
	r, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(h.fd),
		ioctlBulk, uintptr(unsafe.Pointer(&xfer)))
	if errno != 0 {
		return 0, errno
	}
	return int(r), nil
}

// detachKernelDriver sends USBDEVFS_IOCTL with sub-code USBDEVFS_DISCONNECT
// to ask the kernel to release its driver claim on the interface.
func (h *usbHandle) detachKernelDriver(iface int) error {
	req := usbfsIoctl{
		ifno:      int32(iface),
		ioctlCode: ioctlDisconnect,
	}
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(h.fd),
		ioctlUsbfsIoctl, uintptr(unsafe.Pointer(&req)))
	if errno != 0 {
		return errno
	}
	return nil
}

// findDevice scans /sys/bus/usb/devices for a device matching vid:pid and
// returns its bus number, device address, and string descriptors.
func findDevice(vid, pid uint16) (busNum, devNum int, manufacturer, product string, err error) {
	entries, err := os.ReadDir(sysfsUSBDevices)
	if err != nil {
		return 0, 0, "", "", fmt.Errorf("scan usb devices: %w", err)
	}

	wantVID := fmt.Sprintf("%04x", vid)
	wantPID := fmt.Sprintf("%04x", pid)

	for _, e := range entries {
		base := filepath.Join(sysfsUSBDevices, e.Name())

		if readSysfs(base, "idVendor") != wantVID {
			continue
		}
		if readSysfs(base, "idProduct") != wantPID {
			continue
		}

		bus := readSysfsInt(base, "busnum")
		dev := readSysfsInt(base, "devnum")
		if bus == 0 || dev == 0 {
			continue
		}

		mfr := readSysfs(base, "manufacturer")
		prod := readSysfs(base, "product")
		return bus, dev, mfr, prod, nil
	}

	return 0, 0, "", "", fmt.Errorf("device %04x:%04x not found", vid, pid)
}

func readSysfs(base, attr string) string {
	data, err := os.ReadFile(filepath.Join(base, attr))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func readSysfsInt(base, attr string) int {
	s := readSysfs(base, attr)
	if s == "" {
		return 0
	}
	var v int
	// sysfs integers are decimal
	_, err := fmt.Sscanf(s, "%d", &v)
	if err != nil {
		return 0
	}
	return v
}

// isTimeout reports whether err is a Unix ETIMEDOUT or ENODATA (usbfs
// returns ENODATA on interrupt-IN timeout with no data).
func isTimeout(err error) bool {
	return err == unix.ETIMEDOUT || err == unix.ENODATA
}

