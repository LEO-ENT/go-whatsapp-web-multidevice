package whatsapp

import "github.com/sirupsen/logrus"

func logReceiptRead(count int) {
	log.Infof("whatsapp_receipt.read count=%d", count)
}

func logReceiptDelivered(count int) {
	log.Infof("whatsapp_receipt.delivered count=%d", count)
}

func logReceiptLinkedDeviceSkipped() {
	logrus.Debug("whatsapp_receipt.linked_device_skipped")
}

func logReceiptStorageUnavailable() {
	logrus.Warn("chatwoot_receipt.storage_unavailable")
}

func logProviderReceiptStorageUnavailable() {
	logrus.Warn("provider_message_receipt.storage_unavailable")
}

func logChatwootReceiptLookupFailed() {
	logrus.Error("chatwoot_receipt.lookup_failed")
}

func logChatwootReceiptMissingSource() {
	logrus.Debug("chatwoot_receipt.missing_source")
}

func logChatwootReceiptUpdateLastSeenFailed() {
	logrus.Error("chatwoot_receipt.update_last_seen_failed")
}

func logChatwootReceiptMarkReadFailed() {
	logrus.Error("chatwoot_receipt.mark_read_failed")
}
