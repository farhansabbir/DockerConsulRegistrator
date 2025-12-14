package config

type Config struct {
	DockerHost              string            `json:"docker_host"`
	ConsulHost              string            `json:"consul_host"`
	ListenForTagsToRegister map[string]string `json:"listen_for_tags_to_register,omitempty"`
}

func NewConfig(dockerHost, consulHost string, listenForTags ...map[string]string) *Config {
	return &Config{
		DockerHost: dockerHost,
		ConsulHost: consulHost,
	}
}
