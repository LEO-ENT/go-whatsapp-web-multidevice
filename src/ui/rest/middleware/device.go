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

// ProviderLookupOpaqueDeny is the uniform response for every provider lookup
// request that is not an authenticated POST. Keep this identical to the
// provider route's Basic Auth rejection so the path cannot be used as a method
// or device-enumeration oracle.
func ProviderLookupOpaqueDeny(c fiber.Ctx) error {
	c.Set(fiber.HeaderWWWAuthenticate, `Basic realm="Restricted", charset="UTF-8"`)
	c.Set(fiber.HeaderCacheControl, "no-store")
	c.Set(fiber.HeaderVary, fiber.HeaderAuthorization)
	return c.SendStatus(fiber.StatusUnauthorized)
}

// ProviderLookupBoundary is installed by a method-agnostic Fiber Use route on
// the canonical lookup path before every concrete route registration. It marks
// the request as boundary-owned and denies every method except POST without
// calling Next. POST is the sole method allowed to continue to authentication,
// opaque device resolution, and the controller.
func ProviderLookupBoundary() fiber.Handler {
	return func(c fiber.Ctx) error {
		c.Locals(routepath.ProviderLookupBoundaryLocal, true)
		if c.Method() != fiber.MethodPost {
			return ProviderLookupOpaqueDeny(c)
		}
		return c.Next()
	}
}

func deviceMiddleware(dm *whatsapp.DeviceManager, opaqueErrors bool) fiber.Handler {
	return func(c fiber.Ctx) error {
		// Allow non-device-scoped public endpoints (e.g., landing page) to pass through.
		path := strings.TrimSpace(c.Path())
		if path == "/" || path == "" || path == config.AppBasePath || path == config.AppBasePath+"/" {
			return c.Next()
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
				if opaqueErrors {
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
