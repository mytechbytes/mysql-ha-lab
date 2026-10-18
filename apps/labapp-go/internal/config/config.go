package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

const AppName = "labapp-go"

type Config struct {
	HTTPAddr string

	DBHost     string
	WriterPort int // router RW port
	ReaderPort int // router RO port
	DBName     string
	DBUser     string
	DBPassword string
	PoolSize   int

	TxTimeout   time.Duration // deadline for ONE attempt of a transaction
	MaxAttempts int

	ProbeInterval time.Duration
	ProbeTimeout  time.Duration

	LoadEnabled bool
	LoadDelay   time.Duration
}

func Load() (Config, error) {
	var errs []error
	c := Config{
		HTTPAddr:      ":" + env("HTTP_PORT", "8080"),
		DBHost:        env("DB_HOST", "mysql-router"),
		WriterPort:    envInt("DB_WRITER_PORT", 6446, &errs),
		ReaderPort:    envInt("DB_READER_PORT", 6447, &errs),
		DBName:        env("DB_NAME", "labapp_go"),
		DBUser:        env("DB_USER", os.Getenv("LABAPP_GO_DB_USER")),
		DBPassword:    env("DB_PASSWORD", os.Getenv("LABAPP_GO_DB_PASSWORD")),
		PoolSize:      envInt("DB_POOL_SIZE", 10, &errs),
		TxTimeout:     envDuration("TX_TIMEOUT", 10*time.Second, &errs),
		MaxAttempts:   envInt("RETRY_MAX_ATTEMPTS", 8, &errs),
		ProbeInterval: envDuration("PROBE_INTERVAL", time.Second, &errs),
		ProbeTimeout:  envDuration("PROBE_TIMEOUT", 2*time.Second, &errs),
		LoadEnabled:   envBool("LOAD_ENABLED", false, &errs),
		LoadDelay:     envDuration("LOAD_DELAY", 200*time.Millisecond, &errs),
	}
	if c.DBUser == "" || c.DBPassword == "" {
		errs = append(errs, errors.New("DB_USER and DB_PASSWORD (or LABAPP_GO_DB_USER / LABAPP_GO_DB_PASSWORD) must be set"))
	}
	return c, errors.Join(errs...)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int, errs *[]error) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s=%q: not a number", key, v))
	}
	return n
}

func envBool(key string, def bool, errs *[]error) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s=%q: not true/false", key, v))
	}
	return b
}

// envDuration accepts Go durations: 500ms, 2s, 1m.
func envDuration(key string, def time.Duration, errs *[]error) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s=%q: not a duration like 500ms or 2s", key, v))
	}
	return d
}
