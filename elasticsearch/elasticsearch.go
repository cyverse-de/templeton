package elasticsearch

import (
	"fmt"
	"io"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel"

	"github.com/cyverse-de/esutils"
	"gopkg.in/olivere/elastic.v5"

	"context"

	"github.com/cyverse-de/templeton/database"
	"github.com/cyverse-de/templeton/logging"
	"github.com/cyverse-de/templeton/model"
)

var (
	// knownTypes is a mapping which stores known types to index
	knownTypes = map[string]bool{
		"file":   true,
		"folder": true,
	}
	log      = logging.Log.WithFields(logrus.Fields{"package": "elasticsearch"})
	otelName = "github.com/cyverse-de/templeton/elasticsearch"
)

// Elasticer is a type used to interact with Elasticsearch
type Elasticer struct {
	es      *elastic.Client
	baseURL string
	index   string
}

// NewElasticer returns a pointer to an Elasticer instance that has already tested its connection
// by making a WaitForStatus call to the configured Elasticsearch cluster
func NewElasticer(elasticsearchBase string, user string, password string, elasticsearchIndex string) (*Elasticer, error) {
	c, err := elastic.NewSimpleClient(elastic.SetURL(elasticsearchBase), elastic.SetBasicAuth(user, password))

	if err != nil {
		return nil, err
	}

	return &Elasticer{es: c, baseURL: elasticsearchBase, index: elasticsearchIndex}, nil
}

func (e *Elasticer) Close() {
	e.es.Stop()
}

func (e *Elasticer) NewBulkIndexer(context context.Context, bulkSize int) *esutils.BulkIndexer {
	return esutils.NewBulkIndexerContext(context, e.es, bulkSize)
}

func (e *Elasticer) PurgeType(context context.Context, d *database.Databaser, indexer *esutils.BulkIndexer, t string) error {
	ctx, span := otel.Tracer(otelName).Start(context, "PurgeType")
	defer span.End()

	scanner := e.es.Scroll(e.index).Type(t).Scroll("1m")

	for {
		docs, err := scanner.Do(ctx)
		if err == io.EOF {
			log.Infof("Finished all rows for purge of %s.", t)
			break
		}
		if err != nil {
			return err
		}

		if docs.TotalHits() > 0 {
			for _, hit := range docs.Hits.Hits {
				avus, err := d.GetObjectAVUs(hit.Id)
				if err != nil {
					log.Errorf("Error processing %s/%s: %s", t, hit.Id, err)
					continue
				}
				if len(avus) == 0 {
					log.Infof("Deleting %s/%s", t, hit.Id)
					req := elastic.NewBulkDeleteRequest().Index(e.index).Type(t).Routing(hit.Id).Id(hit.Id)
					err = indexer.Add(req)
					if err != nil {
						log.Errorf("Error enqueuing delete of %s/%s: %s", t, hit.Id, err)
					}
				}
			}
		}
	}
	return nil
}

// PurgeIndex walks an index querying a database, deleting those which should not exist
func (e *Elasticer) PurgeIndex(context context.Context, d *database.Databaser) {
	ctx, span := otel.Tracer(otelName).Start(context, "PurgeIndex")
	defer span.End()

	indexer := e.NewBulkIndexer(ctx, 1000)
	defer indexer.Flush()

	err := e.PurgeType(ctx, d, indexer, "file_metadata")
	if err != nil {
		log.Fatal(err)
		return
	}

	err = e.PurgeType(ctx, d, indexer, "folder_metadata")
	if err != nil {
		log.Fatal(err)
		return
	}
}

// IndexEverything creates a bulk indexer and takes a database, and iterates to index its contents
func (e *Elasticer) IndexEverything(context context.Context, d *database.Databaser) {
	ctx, span := otel.Tracer(otelName).Start(context, "IndexEverything")
	defer span.End()

	indexer := e.NewBulkIndexer(ctx, 1000)
	defer indexer.Flush()

	cursor, err := d.GetAllObjects()
	if err != nil {
		log.Fatal(err)
	}
	defer cursor.Close()

	for {
		avus, err := cursor.Next()
		if err == database.EOS {
			log.Info("Done all rows, finishing.")
			break
		}
		if err != nil {
			log.Error(err)
			break
		}

		formatted, err := model.AVUsToIndexedObject(avus)
		if err != nil {
			log.Error(err)
			break
		}

		if knownTypes[avus[0].TargetType] {
			indexedType := fmt.Sprintf("%s_metadata", avus[0].TargetType)
			log.Infof("Indexing %s/%s", indexedType, formatted.ID)

			req := elastic.NewBulkIndexRequest().Index(e.index).Type(indexedType).Parent(formatted.ID).Id(formatted.ID).Doc(formatted)
			err = indexer.Add(req)
			if err != nil {
				log.Error(err)
				break
			}
		}
	}
}

func (e *Elasticer) Reindex(context context.Context, d *database.Databaser) {
	ctx, span := otel.Tracer(otelName).Start(context, "Reindex")
	defer span.End()

	e.PurgeIndex(ctx, d)
	e.IndexEverything(ctx, d)
}

func (e *Elasticer) DeleteOne(context context.Context, id string) {
	ctx, span := otel.Tracer(otelName).Start(context, "DeleteOne")
	defer span.End()

	log.Infof("Deleting metadata for %s", id)
	_, fileErr := e.es.Delete().Index(e.index).Type("file_metadata").Parent(id).Id(id).Do(ctx)
	_, folderErr := e.es.Delete().Index(e.index).Type("folder_metadata").Parent(id).Id(id).Do(ctx)
	if fileErr != nil && folderErr != nil {
		log.Errorf("Error deleting file metadata for %s: %s", id, fileErr)
		log.Errorf("Error deleting folder metadata for %s: %s", id, folderErr)
	}
}

// IndexOne takes a database and one ID and reindexes that one entity. It should not die or throw errors.
func (e *Elasticer) IndexOne(context context.Context, d *database.Databaser, id string) {
	ctx, span := otel.Tracer(otelName).Start(context, "IndexOne")
	defer span.End()

	avus, err := d.GetObjectAVUs(id)
	if err != nil {
		log.Error(err)
		return
	}

	formatted, err := model.AVUsToIndexedObject(avus)
	if err == model.ErrNoAVUs {
		e.DeleteOne(ctx, id)
		return
	}
	if err != nil {
		log.Error(err)
		return
	}

	if knownTypes[avus[0].TargetType] {
		indexedType := fmt.Sprintf("%s_metadata", avus[0].TargetType)
		log.Infof("Indexing %s/%s", indexedType, formatted.ID)
		_, err = e.es.Index().Index(e.index).Type(indexedType).Parent(formatted.ID).Id(formatted.ID).BodyJson(formatted).Do(ctx)
		if err != nil {
			log.Error(err)
		}
	}
}
