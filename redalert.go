package main

import (
	"os"

	"github.com/jonog/redalert/core"
	"github.com/jonog/redalert/web"
)

func main() {

	config, err := ReadConfigFile()
	if err != nil {
		panic("Missing or invalid config")
	}

	service := core.NewService()

	// Setup Alerts
	ConfigureStdErr(service)
	ConfigureGmail(service, config.Gmail)
	ConfigureSlack(service, config.Slack)
	ConfigureTwilio(service, config.Twilio)

	// Setup Servers to Ping
	for _, sc := range config.Servers {
		service.AddServer(sc.Name, sc.Address, sc.Interval, sc.Alerts)
	}

	service.Start()

	go web.Run(service, getPort())

	service.KeepRunning()

}

func getPort() string {
	if os.Getenv("RA_PORT") == "" {
		return "8888"
	}
	return os.Getenv("RA_PORT")
}
