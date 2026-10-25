package catalog

import (
	_ "embed"
)

//go:embed data/full_catalog.json
var embeddedCatalog []byte

func loadEmbedded() (*Catalog, error) {
	return LoadJSON(embeddedCatalog)
}