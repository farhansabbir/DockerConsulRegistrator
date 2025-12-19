package config

import (
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/docker/docker/client"
)

const (
	APP_NAME   = "DockerConsulRegistrator"
	CACHE_FILE = "cache.db"
)

var (
	once sync.Once
	cfg  *Config
)

type Config struct {
	DockerClient            *client.Client    `json:"-"`
	ConsulHost              string            `json:"consul_host"`
	ListenForTagsToRegister map[string]string `json:"listen_for_tags_to_register,omitempty"`
	DoNotRegister           bool              `json:"do_not_register"`
	Logger                  *slog.Logger      `json:"-"`
}

func NewConfig(dockerHost, consulHost string, doNotRegister bool, listenForTags ...map[string]string) *Config {
	once.Do(func() {
		cfg = &Config{
			ConsulHost:    consulHost,
			DoNotRegister: doNotRegister,
			Logger:        slog.Default().With(),
			ListenForTagsToRegister: func() map[string]string {
				if len(listenForTags) > 0 {
					return listenForTags[0]
				}
				return nil
			}(),
		}
	})
	return cfg
}

func GetConfig() *Config {
	return cfg
}

func (conf *Config) SetDockerClient(dockerClient *client.Client) *Config {
	conf.DockerClient = dockerClient
	return conf
}

func (c *Config) String() string {
	js, _ := json.Marshal(c)
	if js != nil {
		return string(js)
	}
	return ""
}
