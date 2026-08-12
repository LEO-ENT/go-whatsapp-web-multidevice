package middleware

import (
	"net/url"
	"strings"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/routepath"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/gofiber/fiber/v3"
)

const DeviceIDHeader = "X-Device-Id"

// DeviceMiddleware fetches a device instance by header (preferred), path param, or query param
// and injects it into the context. It falls back to the default/only device for single-device mode.
func DeviceMiddleware(dm *whatsapp.DeviceManager) fiber.Handler {
	return deviceMiddleware(dm, false)
}

// OpaqueDeviceMiddleware resolves the selected device without reflecting an
// attacker-controlled header/JID in an error response. Use it for private
// reconciliation and other identifier-sensitive routes.
func OpaqueDeviceMiddleware(dm *whatsapp.DeviceManager) fiber.Handler {
	return deviceMiddleware(dm, true)
}

func deviceMiddleware(dm *whatsapp.DeviceManager, opaqueErrors bool) fiber.Handler {
	return func(c fiber.Ctx) error {
		opaqueForRequest := opaqueErrors
		// Allow non-device-scoped public endpoints (e.g., landing page) to pass through.
		path := strings.TrimSpace(c.Path())
		if path == "/" || path == "" || path == config.AppBasePath || path == config.AppBasePath+"/" {
			return c.Next()
		}

		// Provider reconciliation is opaque by path, independent of middleware
		// registration order. A broad compatibility DeviceMiddleware mounted
		// before authentication must defer resolution; after authentication every
		// device middleware on this path uses the non-reflective error shape.
		if routepath.IsProviderLookup(path, config.AppBasePath) {
			if authenticated, _ := c.Locals(routepath.ProviderLookupAuthenticatedLocal).(bool); !authenticated {
				return c.Next()
			}
			opaqueForRequest = true
		}

		if dm == nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(utils.ResponseData{
				Status:  fiber.StatusServiceUnavailable,
				Code:    "DEVICE_MANAGER_UNAVAILABLE",
				Message: "Device manager is not initialized",
				Results: nil,
			})
		}

		deviceID := strings.TrimSpace(c.Get(DeviceIDHeader))
		// URL-decode the header value to support non-ASCII characters
		if decoded, err := url.QueryUnescape(deviceID); err == nil {
			deviceID = decoded
		}
		if deviceID == "" {
			deviceID = strings.TrimSpace(c.Query("device_id"))
		}

		instance, resolvedID, err := dm.ResolveDevice(deviceID)
		if err != nil {
			if resolvedID != "" || strings.TrimSpace(deviceID) != "" {
				if opaqueForRequest {
					return c.Status(fiber.StatusNotFound).JSON(utils.ResponseData{
						Status:  fiber.StatusNotFound,
						Code:    "DEVICE_NOT_FOUND",
						Message: "Selected device is unavailable",
						Results: nil,
					})
				}
				// Compatibility boundary for existing routes. Identifier-sensitive
				// endpoints must use OpaqueDeviceMiddleware instead.
				return c.Status(fiber.StatusNotFound).JSON(utils.ResponseData{
					Status:  fiber.StatusNotFound,
					Code:    "DEVICE_NOT_FOUND",
					Message: "device not found; create a device first from /api/devices or provide a valid X-Device-Id",
					Results: map[string]string{"device_id": resolvedID},
				})
			}

			return c.Status(fiber.StatusBadRequest).JSON(utils.ResponseData{
				Status:  fiber.StatusBadRequest,
				Code:    "DEVICE_ID_REQUIRED",
				Message: "device_id is required via X-Device-Id header or device_id query",
				Results: nil,
			})
		}

		c.Locals("device_id", resolvedID)
		c.Locals("device", instance)
		c.SetContext(whatsapp.ContextWithDevice(c.Context(), instance))
		return c.Next()
	}
}
