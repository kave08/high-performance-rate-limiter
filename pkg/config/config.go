package config

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

type config struct {
	Redis    Redis    `yaml:"Redis"`
	Postgres Postgres `yaml:"Postgres"`
}

type Redis struct {
	Addr         string        `yaml:"Addr"`
	Password     string        `yaml:"Password"`
	DB           int           `yaml:"DB"`
	MaxRetries   int           `yaml:"MaxRetries"`
	DialTimeout  time.Duration `yaml:"DialTimeout"`
	ReadTimeout  time.Duration `yaml:"ReadTimeout"`
	WriteTimeout time.Duration `yaml:"WriteTimeout"`
	PoolSize     int           `yaml:"PoolSize"`
	MinIdleConns int           `yaml:"MinIdleConns"`
}

type Postgres struct {
	Host            string
	Port            int
	User            string
	Password        string
	DBName          string
	SSLMode         string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

type SQLDatabase struct {
	*sql.DB
	ConnectionName string
	DatabaseName   string
}

type SetupResult struct {
	RedisConnection    *redis.Client
	PostgresConnection SQLDatabase
}

var Cfg config

func LoadConfig(configPath string) *SetupResult {
	viper.SetEnvPrefix("RateLimiter")
	viper.AddConfigPath(".")
	viper.AutomaticEnv()
	viper.SetConfigFile(configPath)
	viper.AddConfigPath(".")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	err := viper.MergeInConfig()
	if err != nil {
		fmt.Println("Error in reading config")
		panic(err)
	}

	err = viper.Unmarshal(&Cfg, func(config *mapstructure.DecoderConfig) {
		config.TagName = "yaml"
		config.DecodeHook = mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.StringToSliceHookFunc(","),
			mapstructure.StringToTimeHookFunc(time.RFC3339),
		)
	})
	if err != nil {
		fmt.Println("Error in unmarshaling config")
		panic(err)
	}

	rdb, err := initializeRedisConnection(Cfg.Redis)
	if err != nil {
		panic(fmt.Sprintf("error at connecting to redis. err: %v, connection info: %+v", err, Cfg.Redis))
	}

	pdb, err := initializePostgreSql(Cfg.Postgres)
	if err != nil {
		panic(fmt.Sprintf("error at connecting to postgresql database. err: %v, connection info: %+v", err, Cfg.Postgres))
	}

	return &SetupResult{
		RedisConnection:    rdb,
		PostgresConnection: *pdb,
	}
}
