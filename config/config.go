package config

import (
	"log"
	"os"

	"gopkg.in/yaml.v2"
)

const DefaultLocation = "/etc/kubectyl/config.yml"

type Configuration struct {
	// Determines if wings should be running in debug mode. This value is ignored
	// if the debug flag is passed through the command line arguments.
	Debug bool

	// An identifier for the token which must be included in any requests to the panel
	// so that the token can be looked up correctly.
	AuthenticationTokenId string `json:"token_id" yaml:"token_id"`

	// The token used when performing operations. Requests to this instance must
	// validate against it.
	AuthenticationToken string `json:"token" yaml:"token"`

	System SystemConfiguration `json:"system" yaml:"system"`

	// The location where the panel is running that this daemon should connect to
	// to collect data and send events.
	PanelLocation string                   `json:"remote" yaml:"remote"`
	RemoteQuery   RemoteQueryConfiguration `json:"remote_query" yaml:"remote_query"`
}

// RemoteQueryConfiguration defines the configuration settings for remote requests
// from Wings to the Panel.
type RemoteQueryConfiguration struct {
	// The amount of time in seconds that Wings should allow for a request to the Panel API
	// to complete. If this time passes the request will be marked as failed. If your requests
	// are taking longer than 30 seconds to complete it is likely a performance issue that
	// should be resolved on the Panel, and not something that should be resolved by upping this
	// number.
	Timeout int `default:"30" yaml:"timeout"`
}

type SystemConfiguration struct {
	// Directory where logs for server installations and other wings events are logged.
	LogDirectory string `default:"/var/log/kubectyl" yaml:"log_directory"`

	// Directory where the server data is stored at.
	Data string `default:"/home" yaml:"data"`

	Sftp SftpConfiguration `yaml:"sftp"`
}

// SftpConfiguration defines the configuration of the internal SFTP server.
type SftpConfiguration struct {
	// The bind address of the SFTP server.
	Address string `default:"0.0.0.0" json:"bind_address" yaml:"bind_address"`
	// The bind port of the SFTP server.
	Port int `default:"2022" json:"bind_port" yaml:"bind_port"`
	// If set to true, no write actions will be allowed on the SFTP server.
	ReadOnly bool `default:"false" yaml:"read_only"`
}

func Get() *Configuration {
	// Load the file; returns []byte
	f, err := os.ReadFile(DefaultLocation)
	if err != nil {
		log.Fatalf("error while reading configuration file: %s", err)
	}

	// Create an empty Car to be are target of unmarshalling
	var c Configuration

	// Unmarshal our input YAML file into empty Car (var c)
	if err := yaml.Unmarshal(f, &c); err != nil {
		log.Fatalf("error while trying to unmarshal config file: %s", err)
	}

	return &c
}
