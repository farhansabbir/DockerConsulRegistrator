package config

import "encoding/json"

type Config struct {
	DockerHost              string            `json:"docker_host"`
	ConsulHost              string            `json:"consul_host"`
	ListenForTagsToRegister map[string]string `json:"listen_for_tags_to_register,omitempty"`
	ShouldRegisterAll       bool              `json:"should_register_all"`
}

func NewConfig(dockerHost, consulHost string, shouldRegisterAll bool, listenForTags ...map[string]string) *Config {
	return &Config{
		DockerHost:        dockerHost,
		ConsulHost:        consulHost,
		ShouldRegisterAll: shouldRegisterAll,
		ListenForTagsToRegister: func() map[string]string {
			if len(listenForTags) > 0 {
				return listenForTags[0]
			}
			return nil
		}(),
	}
}

func (c *Config) String() string {
	js, _ := json.Marshal(c)
	if js != nil {
		return string(js)
	}
	return ""
}
