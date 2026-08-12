package whatsapp

import (
	"go.mau.fi/whatsmeow"
	waLog "go.mau.fi/whatsmeow/util/log"
)

const (
	proxyConfiguredEvent     = "whatsapp_proxy.configured"
	proxyConfigurationFailed = "whatsapp_proxy.configuration_failed"
)

func configureOutboundProxy(client *whatsmeow.Client, rawURL string, logger waLog.Logger) {
	if rawURL == "" {
		return
	}
	logProxyConfigurationResult(logger, client.SetProxyAddress(rawURL))
}

func logProxyConfigurationResult(logger waLog.Logger, err error) {
	if err != nil {
		logger.Errorf(proxyConfigurationFailed)
		return
	}
	logger.Infof(proxyConfiguredEvent)
}
