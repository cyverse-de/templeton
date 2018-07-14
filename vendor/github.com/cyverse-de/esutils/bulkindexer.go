package esutils

import (
	"context"

	"gopkg.in/olivere/elastic.v5"
)

type BulkIndexer struct {
	es          *elastic.Client
	bulkSize    int
	bulkService *elastic.BulkService
}

func NewBulkIndexer(es *elastic.Client, bulkSize int) *BulkIndexer {
	return &BulkIndexer{bulkSize: bulkSize, es: es, bulkService: es.Bulk()}
}

func (b *BulkIndexer) Add(r elastic.BulkableRequest) error {
	b.bulkService.Add(r)
	if b.bulkService.NumberOfActions() >= b.bulkSize {
		err := b.Flush()
		if err != nil {
			return err
		}
	}
	return nil
}

func (b *BulkIndexer) Flush() error {
	_, err := b.bulkService.Do(context.TODO())
	if err != nil {
		return err
	}

	b.bulkService = b.es.Bulk()

	return nil
}
