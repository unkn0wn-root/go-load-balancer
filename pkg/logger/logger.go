package logger

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/natefinch/lumberjack"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	managerOnce sync.Once
	initErr     error
)

type Config struct {
	Level            string             `json:"level"`
	OutputPaths      []string           `json:"outputPaths"`
	ErrorOutputPaths []string           `json:"errorOutputPaths"`
	Development      bool               `json:"development"`
	LogToConsole     bool               `json:"logToConsole"`
	Sampling         SamplingConfig     `json:"sampling"`
	EncodingConfig   EncodingConfig     `json:"encodingConfig"`
	LogRotation      LogRotationConfig  `json:"logRotation"`
	Sanitization     SanitizationConfig `json:"sanitization"`
}

type SamplingConfig struct {
	Initial    int `json:"initial"`
	Thereafter int `json:"thereafter"`
}

type EncodingConfig struct {
	TimeKey         string `json:"timeKey"`
	LevelKey        string `json:"levelKey"`
	NameKey         string `json:"nameKey"`
	CallerKey       string `json:"callerKey"`
	MessageKey      string `json:"messageKey"`
	StacktraceKey   string `json:"stacktraceKey"`
	LineEnding      string `json:"lineEnding"`
	LevelEncoder    string `json:"levelEncoder"`
	TimeEncoder     string `json:"timeEncoder"`
	DurationEncoder string `json:"durationEncoder"`
	CallerEncoder   string `json:"callerEncoder"`
}

type LogRotationConfig struct {
	Enabled    bool `json:"enabled"`
	MaxSizeMB  int  `json:"maxSizeMB"`
	MaxBackups int  `json:"maxBackups"`
	MaxAgeDays int  `json:"maxAgeDays"`
	Compress   bool `json:"compress"`
}

// SanitizationConfig configures sensitive field sanitization.
type SanitizationConfig struct {
	SensitiveFields []string `json:"sensitiveFields"`
	Mask            string   `json:"mask"`
}

// Init initializes the loggers based on the configuration file.
// It should be called once at the start of the application.
func Init(configPath string, manager *LoggerManager) error {
	managerOnce.Do(func() {
		var cfgMap map[string]Config
		data, err := os.ReadFile(configPath)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Println("Configuration file not found. Using default logger configurations.")
				// left on purpose
				// No specific loggers defined; use defaults for any logger initialized later
			} else {
				initErr = fmt.Errorf("failed to read configuration file: %w", err)
				return
			}
		} else {
			var configWrapper struct {
				Loggers map[string]Config `json:"loggers"`
			}
			if err := json.Unmarshal(data, &configWrapper); err != nil {
				initErr = fmt.Errorf("failed to parse configuration file: %w", err)
				return
			}
			cfgMap = configWrapper.Loggers
		}

		for name, cfg := range cfgMap {
			logger, err := buildLogger(name, &cfg)
			if err != nil {
				initErr = fmt.Errorf("failed to build logger '%s': %w", name, err)
				return
			}
			manager.AddLogger(name, logger)
		}
	})
	return initErr
}

func buildLogger(name string, cfg *Config) (*zap.Logger, error) {
	// Apply default configuration if any field is missing
	assignDefaultValues(cfg)

	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        cfg.EncodingConfig.TimeKey,
		LevelKey:       cfg.EncodingConfig.LevelKey,
		NameKey:        cfg.EncodingConfig.NameKey,
		CallerKey:      cfg.EncodingConfig.CallerKey,
		MessageKey:     cfg.EncodingConfig.MessageKey,
		StacktraceKey:  cfg.EncodingConfig.StacktraceKey,
		LineEnding:     cfg.EncodingConfig.LineEnding,
		EncodeLevel:    getZapLevelEncoder(cfg.EncodingConfig.LevelEncoder),
		EncodeTime:     getZapTimeEncoder(cfg.EncodingConfig.TimeEncoder),
		EncodeDuration: getZapDurationEncoder(cfg.EncodingConfig.DurationEncoder),
		EncodeCaller:   getZapCallerEncoder(cfg.EncodingConfig.CallerEncoder),
	}

	// Console Encoder with colored levels
	consoleEncoderConfig := encoderConfig
	consoleEncoderConfig.EncodeLevel = coloredLevelEncoder
	consoleEncoder := zapcore.NewConsoleEncoder(consoleEncoderConfig)

	jsonEncoder := zapcore.NewJSONEncoder(encoderConfig)
	atomicLevel := zap.NewAtomicLevelAt(getZapLevel(cfg.Level))

	var allCores []zapcore.Core
	if cfg.Development || cfg.LogToConsole {
		// 1. Console Core - Synchronous
		consoleWS := zapcore.Lock(os.Stdout)
		consoleCore := zapcore.NewCore(consoleEncoder, consoleWS, atomicLevel)
		allCores = append(allCores, consoleCore)
	}

	for _, path := range cfg.OutputPaths {
		if path == "stdout" || path == "stderr" {
			// Already handled by consoleCore
			continue
		}

		var fileWS zapcore.WriteSyncer
		if cfg.LogRotation.Enabled {
			lj := ljLogger(path, cfg.LogRotation)
			fileWS = zapcore.AddSync(lj)
		} else {
			file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				return nil, fmt.Errorf("Failed to open log file '%s': %v\n", path, err)
			}
			fileWS = zapcore.AddSync(file)
		}

		fileCore := zapcore.NewCore(jsonEncoder, fileWS, atomicLevel)
		asyncFileCore := NewAsyncCore(fileCore, 1000, 100, 500*time.Millisecond) // bufferSize, batchSize, flushInterval
		allCores = append(allCores, asyncFileCore)
	}

	combinedCore := zapcore.NewTee(allCores...)

	// apply Sanitization if needed
	if len(cfg.Sanitization.SensitiveFields) > 0 {
		combinedCore = NewSanitizerCore(combinedCore, cfg.Sanitization.SensitiveFields, cfg.Sanitization.Mask)
	}

	logger := zap.New(combinedCore,
		zap.AddCaller(),
		zap.AddStacktrace(zap.ErrorLevel),
	).Named(name)

	return logger, nil
}

// maps string levels to zapcore.Level.
func getZapLevel(level string) zapcore.Level {
	switch strings.ToLower(level) {
	case "debug":
		return zap.DebugLevel
	case "info":
		return zap.InfoLevel
	case "warn", "warning":
		return zap.WarnLevel
	case "error":
		return zap.ErrorLevel
	case "dpanic":
		return zap.DPanicLevel
	case "panic":
		return zap.PanicLevel
	case "fatal":
		return zap.FatalLevel
	default:
		return zap.InfoLevel
	}
}

// maps string encoders to zapcore.LevelEncoder.
func getZapLevelEncoder(encoder string) zapcore.LevelEncoder {
	switch strings.ToLower(encoder) {
	case "lowercase":
		return zapcore.LowercaseLevelEncoder
	case "uppercase":
		return zapcore.CapitalLevelEncoder
	case "capital":
		return zapcore.CapitalLevelEncoder
	default:
		return zapcore.LowercaseLevelEncoder
	}
}

// maps string encoders to zapcore.TimeEncoder.
func getZapTimeEncoder(encoder string) zapcore.TimeEncoder {
	switch strings.ToLower(encoder) {
	case "iso8601":
		return zapcore.ISO8601TimeEncoder
	case "epoch":
		return zapcore.EpochTimeEncoder
	case "millis":
		return zapcore.EpochMillisTimeEncoder
	case "nanos":
		return zapcore.EpochNanosTimeEncoder
	default:
		return zapcore.ISO8601TimeEncoder
	}
}

// maps string encoders to zapcore.DurationEncoder.
func getZapDurationEncoder(encoder string) zapcore.DurationEncoder {
	switch strings.ToLower(encoder) {
	case "string":
		return zapcore.StringDurationEncoder
	case "seconds":
		return zapcore.SecondsDurationEncoder
	case "millis":
		return zapcore.MillisDurationEncoder
	case "nanos":
		return zapcore.NanosDurationEncoder
	default:
		return zapcore.StringDurationEncoder
	}
}

// maps string encoders to zapcore.CallerEncoder.
func getZapCallerEncoder(encoder string) zapcore.CallerEncoder {
	switch strings.ToLower(encoder) {
	case "full":
		return zapcore.FullCallerEncoder
	case "short":
		return zapcore.ShortCallerEncoder
	default:
		return zapcore.ShortCallerEncoder
	}
}

// adds color codes to log levels for console output - this is a bit slow so only in dev
func coloredLevelEncoder(l zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	var level string
	switch l {
	case zapcore.DebugLevel:
		level = "\x1b[36m" + l.String() + "\x1b[0m" // Cyan
	case zapcore.InfoLevel:
		level = "\x1b[32m" + l.String() + "\x1b[0m" // Green
	case zapcore.WarnLevel:
		level = "\x1b[33m" + l.String() + "\x1b[0m" // Yellow
	case zapcore.ErrorLevel:
		level = "\x1b[31m" + l.String() + "\x1b[0m" // Red
	case zapcore.DPanicLevel, zapcore.PanicLevel, zapcore.FatalLevel:
		level = "\x1b[35m" + l.String() + "\x1b[0m" // Magenta
	default:
		level = l.String()
	}
	enc.AppendString(level)
}

// creates a new Lumberjack logger with the given path and configuration.
func ljLogger(path string, l LogRotationConfig) *lumberjack.Logger {
	return &lumberjack.Logger{
		Filename:   path,
		MaxSize:    l.MaxSizeMB,
		MaxBackups: l.MaxBackups,
		MaxAge:     l.MaxAgeDays,
		Compress:   l.Compress,
	}
}
