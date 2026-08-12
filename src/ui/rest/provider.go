package rest

import (
	"errors"

	domainProvider "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/provider"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/routepath"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/gofiber/fiber/v3"
)

type Provider struct {
	Service domainProvider.IMessageLookupUsecase
}

const ProviderLookupPath = routepath.ProviderLookupPath

func NewProvider(service domainProvider.IMessageLookupUsecase) *Provider {
	return &Provider{Service: service}
}

func (controller *Provider) LookupMessage(c fiber.Ctx) error {
	boundary, _ := c.Locals(routepath.ProviderLookupBoundaryLocal).(bool)
	if !boundary {
		panic(pkgError.InternalServerError("Provider message lookup unavailable"))
	}
	var request domainProvider.MessageLookupRequest
	if err := c.Bind().Body(&request); err != nil {
		panic(pkgError.ValidationError("invalid provider message lookup request"))
	}
	result, err := controller.Service.LookupMessage(
		whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)),
		request.ProviderMessageID,
	)
	if err != nil {
		var generic pkgError.GenericError
		if errors.As(err, &generic) {
			panic(generic)
		}
		panic(pkgError.InternalServerError("Provider message lookup failed"))
	}

	return c.JSON(utils.ResponseData{
		Status:  200,
		Code:    "SUCCESS",
		Message: "Provider message lookup completed",
		Results: result,
	})
}
