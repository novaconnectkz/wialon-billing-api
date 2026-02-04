package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

// Config - основная конфигурация приложения
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Wialon   WialonConfig   `yaml:"wialon"`
}

// ServerConfig - настройки HTTP-сервера
type ServerConfig struct {
	Port string `yaml:"port"`
}

// DatabaseConfig - настройки подключения к PostgreSQL
type DatabaseConfig struct {
	Host     string `yaml:"host"`
	Port     string `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	DBName   string `yaml:"dbname"`
	SSLMode  string `yaml:"sslmode"`
}

// WialonConfig - настройки подключения к Wialon
type WialonConfig struct {
	BaseURL string `yaml:"base_url"` // https://hst-api.wialon.com или Local URL
	Token   string `yaml:"token"`
	Type    string `yaml:"type"` // "hosting" или "local"
}

// Load загружает конфигурацию из YAML-файла
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	// Переопределение из переменных окружения
	if envPort := os.Getenv("PORT"); envPort != "" {
		cfg.Server.Port = envPort
	}
	if envDBHost := os.Getenv("DB_HOST"); envDBHost != "" {
		cfg.Database.Host = envDBHost
	}
	if envWialonToken := os.Getenv("WIALON_TOKEN"); envWialonToken != "" {
		cfg.Wialon.Token = envWialonToken
	}

	return &cfg, nil
}
