package config

import (
	"os"
	"strings"

	log "github.com/Kaese72/huemie-lib/logging"
	"github.com/pkg/errors"
	"github.com/spf13/viper"
)

// DatabaseConfig holds the MariaDB connection parameters.
type DatabaseConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
	Database string `mapstructure:"database"`
}

func (c DatabaseConfig) Validate() error {
	if c.Host == "" {
		return errors.New("must supply database host")
	}
	if c.User == "" {
		return errors.New("must supply database user")
	}
	if c.Password == "" {
		return errors.New("must supply database password")
	}
	return nil
}

// EventConfig holds the RabbitMQ connection parameters used to coordinate
// conversation processing across replicas (termination signals, update
// notifications for SSE fan-out).
type EventConfig struct {
	ConnectionString string `mapstructure:"connectionstring"`
}

func (c EventConfig) Validate() error {
	if c.ConnectionString == "" {
		return errors.New("must supply event connection string")
	}
	return nil
}

// DeviceStoreConfig holds the parameters needed to call the device-store
// public API as the bot's own user, per the requirement that "the bot is
// authenticated as its own user, to the system, and makes actions via that
// user." The credential itself is no longer deploy-time config -- see
// internal/identity.Service and AuthenticationConfig below.
type DeviceStoreConfig struct {
	URL string `mapstructure:"url"`
}

func (c DeviceStoreConfig) Validate() error {
	if c.URL == "" {
		return errors.New("must supply device store URL")
	}
	return nil
}

// AuthenticationConfig holds the parameters needed to call the
// authentication service's own REST API (creating the chatbot's identity,
// logging in as it) -- as opposed to AuthConfig above, which is only the
// public key used to verify tokens on chatbot's own inbound requests.
type AuthenticationConfig struct {
	URL string `mapstructure:"url"`
}

func (c AuthenticationConfig) Validate() error {
	if c.URL == "" {
		return errors.New("must supply authentication service URL")
	}
	return nil
}

// AnthropicConfig holds the LLM provider configuration that isn't a
// secret. The API key itself is no longer configured here -- it is stored
// in the database (see restmodels.APIKey / persistence.Persistence's
// APIKey methods) and fetched fresh for every conversation turn.
type AnthropicConfig struct {
	// Model is the Claude model ID used for every conversation turn.
	Model string `mapstructure:"model"`
}

func (c AnthropicConfig) Validate() error {
	if c.Model == "" {
		return errors.New("must supply anthropic model")
	}
	return nil
}

// AuthConfig holds the parameters needed to verify the authentication
// service's RS256-signed `use` tokens on every inbound request, per the
// authentication service's README ("each service that needs authentication
// ... needs the public key of the Authentication Service for signature
// verification").
type AuthConfig struct {
	RSAPublicKeyPath string `mapstructure:"rsa-public-key-path"`
}

func (c AuthConfig) Validate() error {
	if c.RSAPublicKeyPath == "" {
		return errors.New("must supply auth RSA public key path")
	}
	return nil
}

// LockConfig holds the conversation-lock tuning parameters described in the
// README's "Replica Conversation lock" section.
type LockConfig struct {
	// TimeoutSeconds is how long a lock may go without being re-acquired
	// before another replica is allowed to consider it abandoned and take
	// over. The README specifies 300 seconds.
	TimeoutSeconds int `mapstructure:"timeout-seconds"`
	// MaxToolLoopIterations bounds the tool-call <-> LLM round trip loop
	// (README step 6/7) so a misbehaving model cannot hold the conversation
	// lock forever.
	MaxToolLoopIterations int `mapstructure:"max-tool-loop-iterations"`
}

func (c LockConfig) Validate() error {
	if c.TimeoutSeconds <= 0 {
		return errors.New("lock timeout seconds must be positive")
	}
	if c.MaxToolLoopIterations <= 0 {
		return errors.New("max tool loop iterations must be positive")
	}
	return nil
}

type Config struct {
	Database       DatabaseConfig       `mapstructure:"database"`
	Event          EventConfig          `mapstructure:"event"`
	DeviceStore    DeviceStoreConfig    `mapstructure:"device-store"`
	Anthropic      AnthropicConfig      `mapstructure:"anthropic"`
	Auth           AuthConfig           `mapstructure:"auth"`
	Authentication AuthenticationConfig `mapstructure:"authentication"`
	Lock           LockConfig           `mapstructure:"lock"`
}

func (c Config) Validate() error {
	if err := c.Database.Validate(); err != nil {
		return err
	}
	if err := c.Event.Validate(); err != nil {
		return err
	}
	if err := c.DeviceStore.Validate(); err != nil {
		return err
	}
	if err := c.Anthropic.Validate(); err != nil {
		return err
	}
	if err := c.Auth.Validate(); err != nil {
		return err
	}
	if err := c.Authentication.Validate(); err != nil {
		return err
	}
	if err := c.Lock.Validate(); err != nil {
		return err
	}
	return nil
}

var Loaded Config

func init() {
	// We have elected not to use AutomaticEnv() because of https://github.com/spf13/viper/issues/584
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))

	// Database
	viper.BindEnv("database.host")
	viper.BindEnv("database.port")
	viper.SetDefault("database.port", 3306)
	viper.BindEnv("database.user")
	viper.BindEnv("database.password")
	viper.BindEnv("database.database")
	viper.SetDefault("database.database", "chatbot")

	// Event streaming
	viper.BindEnv("event.connectionstring")

	// Device store
	viper.BindEnv("device-store.url")
	viper.SetDefault("device-store.url", "http://device-store:8080")

	// Anthropic
	viper.BindEnv("anthropic.model")
	viper.SetDefault("anthropic.model", "claude-opus-5")

	// Auth (authentication service RS256 public key, for verifying `use` tokens)
	viper.BindEnv("auth.rsa-public-key-path")

	// Authentication service's own API (creating/logging in as the
	// chatbot's identity -- see internal/identity)
	viper.BindEnv("authentication.url")
	viper.SetDefault("authentication.url", "http://authentication:8080")

	// Conversation locking
	viper.BindEnv("lock.timeout-seconds")
	viper.SetDefault("lock.timeout-seconds", 300)
	viper.BindEnv("lock.max-tool-loop-iterations")
	viper.SetDefault("lock.max-tool-loop-iterations", 25)

	if err := viper.Unmarshal(&Loaded); err != nil {
		log.Error(err.Error(), map[string]interface{}{})
		os.Exit(1)
	}
}
