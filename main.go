package main

import (
	"net/http"
	"os"
	"time"

	"github.com/apex/log"
	"github.com/apex/log/handlers/multi"
	"github.com/apex/log/handlers/text"
	"github.com/kubectyl/kuber/remote"
	"github.com/kubectyl/sftp-server/config"
	"github.com/kubectyl/sftp-server/sftp"
)

// Configures the global logger for Zap so that we can call it from any location
// in the code without having to pass around a logger instance.
func initLogging() {
	log.SetLevel(log.InfoLevel)
	if config.Get().Debug {
		log.SetLevel(log.DebugLevel)
	}
	log.SetHandler(multi.New(text.New(os.Stdout)))
	log.Info("writing log file to disk")
}

func main() {
	initLogging()

	pclient := remote.New(
		config.Get().PanelLocation,
		remote.WithCredentials(config.Get().AuthenticationTokenId, config.Get().AuthenticationToken),
		remote.WithHttpClient(&http.Client{
			Timeout: time.Second * time.Duration(config.Get().RemoteQuery.Timeout),
		}),
	)

	// Run the SFTP server.
	if err := sftp.New(pclient).Run(); err != nil {
		log.WithError(err).Fatal("failed to initialize the sftp server")
		return
	}
}
