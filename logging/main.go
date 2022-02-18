package logging

import (
	"log"

	"github.com/sirupsen/logrus"
)

var Log = logrus.WithFields(logrus.Fields{"service": "templeton"})

func SetupLogging(configuredLevel string) {
	var level logrus.Level

	formatter := new(logrus.TextFormatter)
	formatter.TimestampFormat = "2006-01-02 15:04:05.9999"
	formatter.FullTimestamp = true

	switch configuredLevel {
	case "trace":
		level = logrus.TraceLevel
	case "debug":
		level = logrus.DebugLevel
	case "info":
		level = logrus.InfoLevel
	case "warn":
		level = logrus.WarnLevel
	case "error":
		level = logrus.ErrorLevel
	case "fatal":
		level = logrus.FatalLevel
	case "panic":
		level = logrus.PanicLevel
	default:
		log.Fatal("incorrect log level")
	}

	Log.Logger.SetLevel(level)
	Log.Logger.SetFormatter(formatter)
}
