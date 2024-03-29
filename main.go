package main

import (
	"context"
	"flag"
	"net"
	"net/http"

	"encoding/json"
	_ "expvar"
	"fmt"
	"os"

	"github.com/cyverse-de/go-events/ping"
	"github.com/cyverse-de/templeton/database"
	"github.com/cyverse-de/templeton/elasticsearch"
	"github.com/cyverse-de/templeton/logging"
	"github.com/cyverse-de/templeton/model"
	"github.com/sirupsen/logrus"

	"github.com/cyverse-de/configurate"
	"github.com/cyverse-de/messaging/v9"
	"github.com/spf13/viper"
	"github.com/streadway/amqp"

	"github.com/cyverse-de/go-mod/otelutils"
)

const serviceName = "templeton"

const defaultConfig = `
amqp:
  uri: amqp://guest:guest@rabbit:5672/
  queue_prefix: ""

elasticsearch:
  base: http://elasticsearch:9200
  index: data

db:
  uri: postgres://de:notprod@dedb:5432/metadata?sslmode=disable
  schema: public
`

var (
	showVersion = flag.Bool("version", false, "Print version information")
	mode        = flag.String("mode", "", "One of 'periodic', 'incremental', or 'full'. Required except for --version.")
	debugPort   = flag.String("debug-port", "60000", "Listen port for requests to /debug/vars.")
	cfgPath     = flag.String("config", "", "Path to the configuration file. Required except for --version.")
	logLevel    = flag.String("log-level", "info", "One of trace, debug, info, warn, error, fatal, or panic.")

	amqpURI               string
	amqpExchangeName      string
	amqpExchangeType      string
	amqpQueuePrefix       string
	elasticsearchBase     string
	elasticsearchUser     string
	elasticsearchPassword string
	elasticsearchIndex    string
	dbURI                 string
	dbSchema              string
	cfg                   *viper.Viper
)

var log = logging.Log.WithFields(logrus.Fields{"package": "main"})

func init() {
	flag.Parse()

	logging.SetupLogging(*logLevel)
}

func checkMode() {
	validModes := []string{"periodic", "incremental", "full"}
	foundMode := false

	for _, v := range validModes {
		if v == *mode {
			foundMode = true
		}
	}

	if !foundMode {
		fmt.Printf("Invalid mode: %s\n", *mode)
		flag.PrintDefaults()
		os.Exit(-1)
	}
}

func initConfig(cfgPath string) {
	var err error
	cfg, err = configurate.InitDefaults(cfgPath, defaultConfig)
	if err != nil {
		log.Fatal(err)
	}
}

func loadElasticsearchConfig() {
	elasticsearchBase = cfg.GetString("elasticsearch.base")
	elasticsearchUser = cfg.GetString("elasticsearch.user")
	elasticsearchPassword = cfg.GetString("elasticsearch.password")
	elasticsearchIndex = cfg.GetString("elasticsearch.index")
}

func loadAMQPConfig() {
	amqpURI = cfg.GetString("amqp.uri")
	amqpExchangeName = cfg.GetString("amqp.exchange.name")
	amqpExchangeType = cfg.GetString("amqp.exchange.type")
	amqpQueuePrefix = cfg.GetString("amqp.queue_prefix")
}

func loadDBConfig() {
	dbURI = cfg.GetString("db.uri")
	dbSchema = cfg.GetString("db.schema")
}

func doFullMode(es *elasticsearch.Elasticer, d *database.Databaser) {
	log.Info("Full indexing mode selected.")

	es.Reindex(context.Background(), d)
}

// A spinner to keep the program running, since client.Listen() needs to be in a goroutine.
// nolint
func spin() {
	spinner := make(chan int)
	for {
		select {
		case <-spinner:
			fmt.Println("Exiting")
			break
		}
	}
}

func getQueueName(mode, prefix string) string {
	queueName := fmt.Sprintf("%s.%s", serviceName, mode)
	if len(prefix) > 0 {
		queueName = fmt.Sprintf("%s.%s.%s", prefix, serviceName, mode)
	}
	return queueName
}

func doPeriodicMode(es *elasticsearch.Elasticer, d *database.Databaser, client *messaging.Client) {
	log.Info("Periodic indexing mode selected.")

	queueName := getQueueName(*mode, amqpQueuePrefix)
	// Accept and handle messages sent out with the index.all and index.templates routing keys
	client.AddConsumerMulti(
		amqpExchangeName,
		amqpExchangeType,
		queueName,
		[]string{messaging.ReindexAllKey, messaging.ReindexTemplatesKey},
		func(context context.Context, del amqp.Delivery) {
			log.Infof("Received message: [%s] [%s]", del.RoutingKey, del.Body)

			es.Reindex(context, d)
			err := del.Ack(false)
			if err != nil {
				log.Error(err)
			}
		},
		1)

	spin()
}

func doIncrementalMode(es *elasticsearch.Elasticer, d *database.Databaser, client *messaging.Client) {
	log.Info("Incremental indexing mode selected.")

	queueName := getQueueName(*mode, amqpQueuePrefix)
	client.AddConsumer(
		amqpExchangeName,
		amqpExchangeType,
		queueName,
		messaging.IncrementalKey,
		func(context context.Context, del amqp.Delivery) {
			log.Infof("Received message: [%s] [%s]", del.RoutingKey, del.Body)

			var m model.UpdateMessage
			err := json.Unmarshal(del.Body, &m)
			if err != nil {
				log.Error(err)
				err = del.Reject(!del.Redelivered)
				if err != nil {
					log.Error(err)
				}
			}
			es.IndexOne(context, d, m.ID)
			err = del.Ack(false)
			if err != nil {
				log.Infof("Could not ack message: %s", err.Error())
			}
		},
		100)

	spin()
}

func handlePing(client *messaging.Client, delivery amqp.Delivery, mode string) {
	log.Info("Received ping")

	pongKey := fmt.Sprintf("events.templeton.%s.pong", mode)

	out, err := json.Marshal(&ping.Pong{
		PongFrom: fmt.Sprintf("templeton-%s", mode),
	})
	if err != nil {
		log.Error(err)
	}

	log.Info("Sent pong")

	if err = client.Publish(pongKey, out); err != nil {
		log.Error(err)
	}
}

func listenForEvents(client *messaging.Client, mode string) {
	log.Info("Setting up support for events")

	eventsKey := fmt.Sprintf("events.templeton.%s.#", mode)
	pingKey := fmt.Sprintf("events.templeton.%s.ping", mode)

	err := client.SetupPublishing(amqpExchangeName)
	if err != nil {
		log.Fatal(err)
	}

	client.AddConsumer(
		amqpExchangeName,
		amqpExchangeType,
		fmt.Sprintf("events.templeton.%s.queue", mode),
		eventsKey,
		func(context context.Context, delivery amqp.Delivery) {
			err := delivery.Ack(false)
			if err != nil {
				log.Infof("Could not ack message: %s", err.Error())
			}
			log.Infof("Received event message: [%s] [%s]", delivery.RoutingKey, delivery.Body)
			switch delivery.RoutingKey {
			case pingKey:
				handlePing(client, delivery, mode)
			default:
				log.Infof("No handler for message: [%s] [%s]", delivery.RoutingKey, delivery.Body)
			}
		},
		100,
	)
}

func exportVars(port string) {
	go func() {
		sock, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%s", port))
		if err != nil {
			log.Fatal(err)
		}
		err = http.Serve(sock, nil)
		if err != nil {
			log.Fatal(err)
		}
	}()
}

var (
	gitref  string
	appver  string
	builtby string
)

// AppVersion prints the version information to stdout
func AppVersion() {
	if appver != "" {
		fmt.Printf("App-Version: %s\n", appver)
	}
	if gitref != "" {
		fmt.Printf("Git-Ref: %s\n", gitref)
	}
	if builtby != "" {
		fmt.Printf("Built-By: %s\n", builtby)
	}
}

func main() {
	var tracerCtx, cancel = context.WithCancel(context.Background())
	defer cancel()
	shutdown := otelutils.TracerProviderFromEnv(tracerCtx, serviceName, func(e error) { log.Fatal(e) })
	defer shutdown()

	if *showVersion {
		AppVersion()
		os.Exit(0)
	}

	checkMode()

	if *cfgPath == "" {
		fmt.Println("--config is required")
		flag.PrintDefaults()
		os.Exit(-1)
	}

	initConfig(*cfgPath)
	loadElasticsearchConfig()
	es, err := elasticsearch.NewElasticer(elasticsearchBase, elasticsearchUser, elasticsearchPassword, elasticsearchIndex)
	if err != nil {
		log.Fatal(err)
	}
	defer es.Close()

	loadDBConfig()
	d, err := database.NewDatabaser(dbURI, dbSchema)
	if err != nil {
		log.Fatal(err)
	}

	if *mode == "full" {
		doFullMode(es, d)
		return
	}

	loadAMQPConfig()

	client, err := messaging.NewClient(amqpURI, true)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	exportVars(*debugPort)

	go client.Listen()

	listenForEvents(client, *mode)

	if *mode == "periodic" {
		doPeriodicMode(es, d, client)
	}

	if *mode == "incremental" {
		doIncrementalMode(es, d, client)
	}
}
