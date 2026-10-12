package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/mantonx/nexus-open/internal/device"
)

// BrightnessRequest represents a request to set device brightness.
// openapi:schema BrightnessRequest
type BrightnessRequest struct {
	// openapi:description Brightness level (0-100)
	// openapi:example 75
	Brightness int `json:"brightness"`
}

// handleBrightness handles brightness control (POST only).
// openapi:operation POST /api/device/brightness setBrightness
// openapi:summary Set display brightness
// openapi:description Sets the brightness of the iCUE Nexus device display (0-100)
// openapi:tag Device
// openapi:consumes application/json
// openapi:produces application/json
// openapi:param brightness body BrightnessRequest true --- Brightness level to set
// openapi:response 200 SuccessResponse --- Brightness updated successfully
// openapi:response 400 ErrorResponse --- Invalid brightness value
// openapi:response 503 ErrorResponse --- Device not available
func (s *Server) handleBrightness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.respondError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.device == nil {
		s.respondError(w, "Device not available", http.StatusServiceUnavailable)
		return
	}

	var request struct {
		Brightness int `json:"brightness"`
	}

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		s.respondError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if request.Brightness < 0 || request.Brightness > 100 {
		s.respondError(w, "Brightness must be between 0 and 100", http.StatusBadRequest)
		return
	}

	if err := s.device.SetBrightness(request.Brightness); err != nil {
		s.logger.Error("failed to set brightness", "error", err)
		s.respondError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.respondSuccess(w, "Brightness updated", map[string]int{"brightness": request.Brightness})
}

// handleDeviceInfo handles device information queries (GET only).
// openapi:operation GET /api/device/info getDeviceInfo
// openapi:summary Get device information
// openapi:description Returns information about the connected iCUE Nexus device including firmware version and hardware details
// openapi:tag Device
// openapi:produces application/json
// openapi:response 200 SuccessResponse --- Device information retrieved successfully
// openapi:response 503 ErrorResponse --- Device not available
func (s *Server) handleDeviceInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.respondError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.device == nil {
		s.respondError(w, "Device not available", http.StatusServiceUnavailable)
		return
	}

	di := s.device.GetDeviceInfo()
	info := map[string]interface{}{
		"firmware":     "N/A",
		"vendorId":     fmt.Sprintf("0x%04x", di.VendorID),
		"productId":    fmt.Sprintf("0x%04x", di.ProductID),
		"model":        di.Product,
		"manufacturer": di.Manufacturer,
	}

	// Best-effort firmware version from feature report 5.
	if firmware, err := s.device.GetFirmwareVersion(); err == nil {
		info["firmware"] = firmware
	}

	// Surface the last connect error as a human-readable action hint.
	if s.lastConnectErr != nil {
		switch {
		case errors.Is(s.lastConnectErr, device.ErrPermissionDenied):
			info["connect_error"] = "USB permission denied. Run: sudo usermod -a -G plugdev $USER then log out and back in."
		case errors.Is(s.lastConnectErr, device.ErrDeviceBusy):
			info["connect_error"] = "Device is in use by another application. Close iCUE or other Nexus software."
		case errors.Is(s.lastConnectErr, device.ErrDeviceNotFound):
			info["connect_error"] = "iCUE Nexus not found. Is it plugged in?"
		default:
			info["connect_error"] = s.lastConnectErr.Error()
		}
	}

	s.respondSuccess(w, "Device information", info)
}
