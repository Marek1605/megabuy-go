package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
}

type Product struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	Slug             string   `json:"slug"`
	Description      string   `json:"description,omitempty"`
	ShortDescription string   `json:"short_description,omitempty"`
	EAN              string   `json:"ean,omitempty"`
	SKU              string   `json:"sku,omitempty"`
	Brand            string   `json:"brand,omitempty"`
	CategoryID       string   `json:"category_id,omitempty"`
	CategoryName     string   `json:"category_name,omitempty"`
	CategorySlug     string   `json:"category_slug,omitempty"`
	ImageURL         string   `json:"image_url,omitempty"`
	PriceMin         float64  `json:"price_min"`
	PriceMax         float64  `json:"price_max"`
	StockStatus      string   `json:"stock_status"`
	IsActive         bool     `json:"is_active"`
	IsFeatured       bool     `json:"is_featured"`
	Attributes       []Attr   `json:"attributes,omitempty"`
	CreatedAt        string   `json:"created_at"`
}

type Attr struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type SearchResult struct {
	Products   []Product         `json:"products"`
	Total      int64             `json:"total"`
	Facets     map[string][]Facet `json:"facets,omitempty"`
	Took       int64             `json:"took_ms"`
}

type Facet struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

func New() *Client {
	url := os.Getenv("ELASTICSEARCH_URL")
	if url == "" {
		url = "http://localhost:9200"
	}
	return &Client{
		baseURL: url,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// CreateIndex creates the products index with proper mappings
func (c *Client) CreateIndex() error {
	mapping := map[string]interface{}{
		"settings": map[string]interface{}{
			"number_of_shards":   3,
			"number_of_replicas": 0,
			"analysis": map[string]interface{}{
				"analyzer": map[string]interface{}{
					"slovak_analyzer": map[string]interface{}{
						"type":      "custom",
						"tokenizer": "standard",
						"filter":    []string{"lowercase", "asciifolding"},
					},
				},
			},
		},
		"mappings": map[string]interface{}{
			"properties": map[string]interface{}{
				"id":                map[string]string{"type": "keyword"},
				"title":             map[string]interface{}{"type": "text", "analyzer": "slovak_analyzer", "fields": map[string]interface{}{"keyword": map[string]string{"type": "keyword"}}},
				"slug":              map[string]string{"type": "keyword"},
				"description":       map[string]interface{}{"type": "text", "analyzer": "slovak_analyzer"},
				"short_description": map[string]interface{}{"type": "text", "analyzer": "slovak_analyzer"},
				"ean":               map[string]string{"type": "keyword"},
				"sku":               map[string]string{"type": "keyword"},
				"brand":             map[string]interface{}{"type": "text", "fields": map[string]interface{}{"keyword": map[string]string{"type": "keyword"}}},
				"category_id":       map[string]string{"type": "keyword"},
				"category_name":     map[string]interface{}{"type": "text", "fields": map[string]interface{}{"keyword": map[string]string{"type": "keyword"}}},
				"category_slug":     map[string]string{"type": "keyword"},
				"image_url":         map[string]string{"type": "keyword", "index": "false"},
				"price_min":         map[string]string{"type": "float"},
				"price_max":         map[string]string{"type": "float"},
				"stock_status":      map[string]string{"type": "keyword"},
				"is_active":         map[string]string{"type": "boolean"},
				"is_featured":       map[string]string{"type": "boolean"},
				"attributes": map[string]interface{}{
					"type": "nested",
					"properties": map[string]interface{}{
						"name":  map[string]string{"type": "keyword"},
						"value": map[string]string{"type": "keyword"},
					},
				},
				"created_at": map[string]string{"type": "date"},
			},
		},
	}

	body, _ := json.Marshal(mapping)
	req, _ := http.NewRequest("PUT", c.baseURL+"/products", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 && resp.StatusCode != 400 { // 400 = already exists
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to create index: %s", string(body))
	}

	return nil
}

// IndexProduct indexes a single product
func (c *Client) IndexProduct(product Product) error {
	body, _ := json.Marshal(product)
	req, _ := http.NewRequest("PUT", fmt.Sprintf("%s/products/_doc/%s", c.baseURL, product.ID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}

// BulkIndex indexes multiple products at once (much faster for imports)
func (c *Client) BulkIndex(products []Product) error {
	if len(products) == 0 {
		return nil
	}

	var buf bytes.Buffer
	for _, p := range products {
		meta := fmt.Sprintf(`{"index":{"_index":"products","_id":"%s"}}`, p.ID)
		buf.WriteString(meta + "\n")
		doc, _ := json.Marshal(p)
		buf.Write(doc)
		buf.WriteString("\n")
	}

	req, _ := http.NewRequest("POST", c.baseURL+"/_bulk", &buf)
	req.Header.Set("Content-Type", "application/x-ndjson")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}

// Search performs a search with filters and facets
func (c *Client) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	query := c.buildQuery(params)

	body, _ := json.Marshal(query)
	req, _ := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/products/_search", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var esResp esSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&esResp); err != nil {
		return nil, err
	}

	result := &SearchResult{
		Total:    esResp.Hits.Total.Value,
		Took:     esResp.Took,
		Products: make([]Product, 0, len(esResp.Hits.Hits)),
		Facets:   make(map[string][]Facet),
	}

	for _, hit := range esResp.Hits.Hits {
		result.Products = append(result.Products, hit.Source)
	}

	// Parse facets
	if esResp.Aggregations.Categories.Buckets != nil {
		for _, b := range esResp.Aggregations.Categories.Buckets {
			result.Facets["categories"] = append(result.Facets["categories"], Facet{Value: b.Key, Count: b.DocCount})
		}
	}
	if esResp.Aggregations.Brands.Buckets != nil {
		for _, b := range esResp.Aggregations.Brands.Buckets {
			result.Facets["brands"] = append(result.Facets["brands"], Facet{Value: b.Key, Count: b.DocCount})
		}
	}
	if esResp.Aggregations.PriceRanges.Buckets != nil {
		for _, b := range esResp.Aggregations.PriceRanges.Buckets {
			result.Facets["price_ranges"] = append(result.Facets["price_ranges"], Facet{Value: b.Key, Count: b.DocCount})
		}
	}

	return result, nil
}

type SearchParams struct {
	Query      string   `json:"q"`
	CategoryID string   `json:"category_id"`
	Brand      string   `json:"brand"`
	PriceMin   float64  `json:"price_min"`
	PriceMax   float64  `json:"price_max"`
	InStock    bool     `json:"in_stock"`
	Sort       string   `json:"sort"` // price_asc, price_desc, newest, relevance
	Page       int      `json:"page"`
	Limit      int      `json:"limit"`
}

func (c *Client) buildQuery(params SearchParams) map[string]interface{} {
	if params.Page < 1 {
		params.Page = 1
	}
	if params.Limit < 1 || params.Limit > 100 {
		params.Limit = 20
	}

	from := (params.Page - 1) * params.Limit

	// Build bool query
	must := []map[string]interface{}{}
	filter := []map[string]interface{}{
		{"term": map[string]bool{"is_active": true}},
	}

	// Full-text search
	if params.Query != "" {
		must = append(must, map[string]interface{}{
			"multi_match": map[string]interface{}{
				"query":  params.Query,
				"fields": []string{"title^3", "description", "short_description", "brand^2", "ean^4", "sku^4"},
				"type":   "best_fields",
				"fuzziness": "AUTO",
			},
		})
	}

	// Filters
	if params.CategoryID != "" {
		filter = append(filter, map[string]interface{}{
			"term": map[string]string{"category_id": params.CategoryID},
		})
	}
	if params.Brand != "" {
		filter = append(filter, map[string]interface{}{
			"term": map[string]string{"brand.keyword": params.Brand},
		})
	}
	if params.PriceMin > 0 {
		filter = append(filter, map[string]interface{}{
			"range": map[string]interface{}{"price_min": map[string]float64{"gte": params.PriceMin}},
		})
	}
	if params.PriceMax > 0 {
		filter = append(filter, map[string]interface{}{
			"range": map[string]interface{}{"price_max": map[string]float64{"lte": params.PriceMax}},
		})
	}
	if params.InStock {
		filter = append(filter, map[string]interface{}{
			"term": map[string]string{"stock_status": "instock"},
		})
	}

	// Sorting
	sort := []map[string]interface{}{}
	switch params.Sort {
	case "price_asc":
		sort = append(sort, map[string]interface{}{"price_min": "asc"})
	case "price_desc":
		sort = append(sort, map[string]interface{}{"price_min": "desc"})
	case "newest":
		sort = append(sort, map[string]interface{}{"created_at": "desc"})
	default:
		if params.Query != "" {
			sort = append(sort, map[string]interface{}{"_score": "desc"})
		} else {
			sort = append(sort, map[string]interface{}{"created_at": "desc"})
		}
	}

	query := map[string]interface{}{
		"from": from,
		"size": params.Limit,
		"query": map[string]interface{}{
			"bool": map[string]interface{}{
				"must":   must,
				"filter": filter,
			},
		},
		"sort": sort,
		"aggs": map[string]interface{}{
			"categories": map[string]interface{}{
				"terms": map[string]interface{}{
					"field": "category_name.keyword",
					"size":  50,
				},
			},
			"brands": map[string]interface{}{
				"terms": map[string]interface{}{
					"field": "brand.keyword",
					"size":  50,
				},
			},
			"price_ranges": map[string]interface{}{
				"range": map[string]interface{}{
					"field": "price_min",
					"ranges": []map[string]interface{}{
						{"key": "0-50", "to": 50},
						{"key": "50-100", "from": 50, "to": 100},
						{"key": "100-500", "from": 100, "to": 500},
						{"key": "500-1000", "from": 500, "to": 1000},
						{"key": "1000+", "from": 1000},
					},
				},
			},
		},
	}

	return query
}

// DeleteProduct removes a product from the index
func (c *Client) DeleteProduct(id string) error {
	req, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/products/_doc/%s", c.baseURL, id), nil)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// Refresh forces Elasticsearch to make recent changes searchable
func (c *Client) Refresh() error {
	req, _ := http.NewRequest("POST", c.baseURL+"/products/_refresh", nil)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// ES response types
type esSearchResponse struct {
	Took int64 `json:"took"`
	Hits struct {
		Total struct {
			Value int64 `json:"value"`
		} `json:"total"`
		Hits []struct {
			Source Product `json:"_source"`
		} `json:"hits"`
	} `json:"hits"`
	Aggregations struct {
		Categories  esBucketAgg `json:"categories"`
		Brands      esBucketAgg `json:"brands"`
		PriceRanges esBucketAgg `json:"price_ranges"`
	} `json:"aggregations"`
}

type esBucketAgg struct {
	Buckets []struct {
		Key      string `json:"key"`
		DocCount int64  `json:"doc_count"`
	} `json:"buckets"`
}

// DeleteIndex deletes the products index
func (c *Client) DeleteIndex() error {
	if c.httpClient == nil {
		return nil
	}
	req, err := http.NewRequest("DELETE", fmt.Sprintf("%s/%s", c.baseURL, c.index), nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}
