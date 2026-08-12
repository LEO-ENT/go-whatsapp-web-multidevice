package usecase

import (
	"context"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	domainProvider "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/provider"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/validations"
	"github.com/sirupsen/logrus"
)

type serviceProvider struct {
	chatStorageRepo domainChatStorage.IChatStorageRepository
}

func NewProviderService(chatStorageRepo domainChatStorage.IChatStorageRepository) domainProvider.IMessageLookupUsecase {
	return &serviceProvider{chatStorageRepo: chatStorageRepo}
}

// LookupMessage reconciles one deterministic provider ID against durable,
// device-scoped evidence. Any unavailable or internally inconsistent authority
// fails closed to UNKNOWN without rendering provider data or backend errors.
func (service serviceProvider) LookupMessage(ctx context.Context, providerMessageID string) (domainProvider.MessageLookup, error) {
	unknown := domainProvider.MessageLookup{Status: domainProvider.MessageUnknown}
	if err := validations.ValidateProviderMessageID(providerMessageID); err != nil {
		return unknown, err
	}

	deviceID := deviceIDFromContext(ctx)
	if deviceID == "" || service.chatStorageRepo == nil {
		return unknown, nil
	}

	result, err := service.chatStorageRepo.LookupProviderMessage(ctx, deviceID, providerMessageID)
	if err != nil {
		logrus.Warn("provider_message_lookup.storage_unavailable")
		return unknown, nil
	}

	switch result.Status {
	case domainProvider.MessagePresent:
		if result.Receipt == nil || result.Receipt.ProviderMessageID != providerMessageID {
			logrus.Warn("provider_message_lookup.invalid_authority_result")
			return unknown, nil
		}
		return result, nil
	case domainProvider.MessageProvablyAbsent:
		if result.Receipt != nil {
			logrus.Warn("provider_message_lookup.invalid_authority_result")
			return unknown, nil
		}
		return result, nil
	case domainProvider.MessageUnknown:
		return unknown, nil
	default:
		logrus.Warn("provider_message_lookup.invalid_authority_result")
		return unknown, nil
	}
}
