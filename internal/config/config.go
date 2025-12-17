package config

import (
	"encoding/json"
	"sync"
)

var (
	once sync.Once
	cfg  *Config
)

type Config struct {
	DockerHost              string            `json:"docker_host"`
	ConsulHost              string            `json:"consul_host"`
	ListenForTagsToRegister map[string]string `json:"listen_for_tags_to_register,omitempty"`
	DoNotRegister           bool              `json:"do_not_register"`
}

func NewConfig(dockerHost, consulHost string, doNotRegister bool, listenForTags ...map[string]string) *Config {
	once.Do(func() {
		cfg = &Config{
			DockerHost:    dockerHost,
			ConsulHost:    consulHost,
			DoNotRegister: doNotRegister,
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

func (c *Config) String() string {
	js, _ := json.Marshal(c)
	if js != nil {
		return string(js)
	}
	return ""
}
