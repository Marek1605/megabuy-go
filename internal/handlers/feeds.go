package handlers

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	
	"megabuy-go/internal/elasticsearch"
)

// Feed structure
type Feed struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	URL          string            `json:"url"`
	Type         string            `json:"type"`
	VendorID     string            `json:"vendor_id,omitempty"`
	Schedule     string            `json:"schedule"`
	IsActive     bool              `json:"is_active"`
	XMLItemPath  string            `json:"xml_item_path,omitempty"`
	FieldMapping map[string]string `json:"field_mapping,omitempty"`
	LastRun      *time.Time        `json:"last_run,omitempty"`
	LastStatus   string            `json:"last_status,omitempty"`
	ProductCount int               `json:"product_count"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

type FeedPreview struct {
	Fields       []string                 `json:"fields"`
	Sample       []map[string]interface{} `json:"sample"`
	TotalItems   int                      `json:"total_items"`
	DetectedType string                   `json:"detected_type,omitempty"`
}

type ImportProgress struct {
	FeedID    string   `json:"feed_id"`
	Status    string   `json:"status"`
	Message   string   `json:"message"`
	Total     int      `json:"total"`
	Processed int      `json:"processed"`
	Created   int      `json:"created"`
	Updated   int      `json:"updated"`
	Skipped   int      `json:"skipped"`
	Errors    int      `json:"errors"`
	Percent   int      `json:"percent"`
	Logs      []string `json:"logs"`
}

var (
	importProgress = make(map[string]*ImportProgress)
	progressMutex  sync.RWMutex
)

func (h *Handlers) GetFeeds(c *fiber.Ctx) error {
	ctx := context.Background()
	rows, err := h.db.Pool.Query(ctx, `
		SELECT id, name, url, type, COALESCE(vendor_id::text,''), schedule, is_active,
		       COALESCE(xml_item_path,'SHOPITEM'), COALESCE(field_mapping::text,'{}'),
		       last_run, COALESCE(last_status,'idle'), product_count, created_at, updated_at
		FROM feeds ORDER BY created_at DESC
	`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error()})
	}
	defer rows.Close()

	var feeds []Feed
	for rows.Next() {
		var f Feed
		var fieldMappingStr, vendorID string
		rows.Scan(&f.ID, &f.Name, &f.URL, &f.Type, &vendorID, &f.Schedule, &f.IsActive,
			&f.XMLItemPath, &fieldMappingStr, &f.LastRun, &f.LastStatus, &f.ProductCount,
			&f.CreatedAt, &f.UpdatedAt)
		if vendorID != "" {
			f.VendorID = vendorID
		}
		json.Unmarshal([]byte(fieldMappingStr), &f.FieldMapping)
		feeds = append(feeds, f)
	}
	if feeds == nil {
		feeds = []Feed{}
	}
	return c.JSON(fiber.Map{"success": true, "data": feeds})
}

func (h *Handlers) CreateFeed(c *fiber.Ctx) error {
	var input struct {
		Name         string            `json:"name"`
		URL          string            `json:"url"`
		Type         string            `json:"type"`
		VendorID     string            `json:"vendor_id"`
		Schedule     string            `json:"schedule"`
		IsActive     bool              `json:"is_active"`
		XMLItemPath  string            `json:"xml_item_path"`
		FieldMapping map[string]string `json:"field_mapping"`
	}
	if err := c.BodyParser(&input); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "Invalid request"})
	}
	if input.Name == "" || input.URL == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "Name and URL required"})
	}
	if input.Type == "" {
		input.Type = "xml"
	}
	if input.Schedule == "" {
		input.Schedule = "daily"
	}
	if input.XMLItemPath == "" {
		input.XMLItemPath = "SHOPITEM"
	}

	ctx := context.Background()
	feedID := uuid.New()
	fieldMappingJSON, _ := json.Marshal(input.FieldMapping)

	var vendorID interface{} = nil
	if input.VendorID != "" {
		vendorID = input.VendorID
	}

	_, err := h.db.Pool.Exec(ctx, `
		INSERT INTO feeds (id, name, url, type, vendor_id, schedule, is_active, xml_item_path, field_mapping, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5::uuid, $6, $7, $8, $9::jsonb, NOW(), NOW())
	`, feedID, input.Name, input.URL, input.Type, vendorID, input.Schedule, input.IsActive, input.XMLItemPath, string(fieldMappingJSON))
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error()})
	}
	return c.Status(201).JSON(fiber.Map{"success": true, "data": fiber.Map{"id": feedID.String()}})
}

func (h *Handlers) UpdateFeed(c *fiber.Ctx) error {
	feedID := c.Params("id")
	var input struct {
		Name         string            `json:"name"`
		URL          string            `json:"url"`
		Type         string            `json:"type"`
		VendorID     string            `json:"vendor_id"`
		Schedule     string            `json:"schedule"`
		IsActive     bool              `json:"is_active"`
		XMLItemPath  string            `json:"xml_item_path"`
		FieldMapping map[string]string `json:"field_mapping"`
	}
	if err := c.BodyParser(&input); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "Invalid request"})
	}

	ctx := context.Background()
	fieldMappingJSON, _ := json.Marshal(input.FieldMapping)
	var vendorID interface{} = nil
	if input.VendorID != "" {
		vendorID = input.VendorID
	}

	_, err := h.db.Pool.Exec(ctx, `
		UPDATE feeds SET name=$2, url=$3, type=$4, vendor_id=$5::uuid, schedule=$6, 
		       is_active=$7, xml_item_path=$8, field_mapping=$9::jsonb, updated_at=NOW()
		WHERE id=$1::uuid
	`, feedID, input.Name, input.URL, input.Type, vendorID, input.Schedule, input.IsActive, input.XMLItemPath, string(fieldMappingJSON))
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error()})
	}
	return c.JSON(fiber.Map{"success": true, "message": "Feed updated"})
}

func (h *Handlers) DeleteFeed(c *fiber.Ctx) error {
	feedID := c.Params("id")
	ctx := context.Background()
	_, err := h.db.Pool.Exec(ctx, "DELETE FROM feeds WHERE id=$1::uuid", feedID)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error()})
	}
	return c.JSON(fiber.Map{"success": true, "message": "Feed deleted"})
}

func (h *Handlers) PreviewFeed(c *fiber.Ctx) error {
	var input struct {
		URL         string `json:"url"`
		Type        string `json:"type"`
		XMLItemPath string `json:"xml_item_path"`
	}
	if err := c.BodyParser(&input); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "Invalid request"})
	}
	if input.URL == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "URL required"})
	}

	client := &http.Client{Timeout: 30 * time.Second}
	req, _ := http.NewRequest("GET", input.URL, nil)
	req.Header.Set("User-Agent", "MegaBuy Feed Parser/1.0")
	req.Header.Set("Range", "bytes=0-204800")

	resp, err := client.Do(req)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "Cannot download feed: " + err.Error()})
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)

	detectedType := input.Type
	if detectedType == "" {
		trimmed := bytes.TrimSpace(data)
		if bytes.HasPrefix(trimmed, []byte("<?xml")) || bytes.HasPrefix(trimmed, []byte("<")) {
			detectedType = "xml"
		} else if bytes.HasPrefix(trimmed, []byte("[")) || bytes.HasPrefix(trimmed, []byte("{")) {
			detectedType = "json"
		} else {
			detectedType = "csv"
		}
	}

	var preview FeedPreview
	switch detectedType {
	case "xml":
		preview = parseXMLPreview(data, input.XMLItemPath)
	case "json":
		preview = parseJSONPreview(data)
	case "csv":
		preview = parseCSVPreview(data)
	}
	preview.DetectedType = detectedType

	return c.JSON(fiber.Map{"success": true, "data": preview})
}

func parseXMLPreview(data []byte, itemPath string) FeedPreview {
	preview := FeedPreview{Fields: []string{}, Sample: []map[string]interface{}{}}
	if itemPath == "" {
		itemPath = "SHOPITEM"
	}

	decoder := xml.NewDecoder(bytes.NewReader(data))
	var currentItem map[string]interface{}
	var currentElement string
	var inItem bool
	fieldsMap := make(map[string]bool)

	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch t := token.(type) {
		case xml.StartElement:
			if strings.EqualFold(t.Name.Local, itemPath) {
				inItem = true
				currentItem = make(map[string]interface{})
			} else if inItem {
				currentElement = t.Name.Local
			}
		case xml.CharData:
			if inItem && currentElement != "" {
				value := strings.TrimSpace(string(t))
				if value != "" {
					currentItem[currentElement] = value
					fieldsMap[currentElement] = true
				}
			}
		case xml.EndElement:
			if strings.EqualFold(t.Name.Local, itemPath) {
				inItem = false
				if len(currentItem) > 0 {
					preview.Sample = append(preview.Sample, currentItem)
					preview.TotalItems++
					if len(preview.Sample) >= 5 {
						for field := range fieldsMap {
							preview.Fields = append(preview.Fields, field)
						}
						return preview
					}
				}
				currentItem = nil
			}
			currentElement = ""
		}
	}
	for field := range fieldsMap {
		preview.Fields = append(preview.Fields, field)
	}
	return preview
}

func parseJSONPreview(data []byte) FeedPreview {
	preview := FeedPreview{Fields: []string{}, Sample: []map[string]interface{}{}}
	var jsonData interface{}
	if err := json.Unmarshal(data, &jsonData); err != nil {
		return preview
	}

	var items []map[string]interface{}
	switch v := jsonData.(type) {
	case []interface{}:
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				items = append(items, m)
			}
		}
	case map[string]interface{}:
		for _, key := range []string{"products", "items", "data", "results", "offers"} {
			if arr, ok := v[key].([]interface{}); ok {
				for _, item := range arr {
					if m, ok := item.(map[string]interface{}); ok {
						items = append(items, m)
					}
				}
				break
			}
		}
	}

	fieldsMap := make(map[string]bool)
	for i, item := range items {
		if i >= 5 {
			break
		}
		preview.Sample = append(preview.Sample, item)
		for key := range item {
			fieldsMap[key] = true
		}
	}
	for field := range fieldsMap {
		preview.Fields = append(preview.Fields, field)
	}
	preview.TotalItems = len(items)
	return preview
}

func parseCSVPreview(data []byte) FeedPreview {
	preview := FeedPreview{Fields: []string{}, Sample: []map[string]interface{}{}}
	firstLine := strings.Split(string(data), "\n")[0]
	delimiter := ';'
	if strings.Count(firstLine, ",") > strings.Count(firstLine, ";") {
		delimiter = ','
	}

	reader := csv.NewReader(bytes.NewReader(data))
	reader.Comma = delimiter
	reader.LazyQuotes = true

	header, err := reader.Read()
	if err != nil {
		return preview
	}
	preview.Fields = header

	for i := 0; i < 5; i++ {
		row, err := reader.Read()
		if err != nil {
			break
		}
		item := make(map[string]interface{})
		for j, val := range row {
			if j < len(header) {
				item[header[j]] = val
			}
		}
		preview.Sample = append(preview.Sample, item)
		preview.TotalItems++
	}
	return preview
}

func (h *Handlers) StartImport(c *fiber.Ctx) error {
	feedID := c.Params("id")
	ctx := context.Background()

	var feed Feed
	var fieldMappingStr string
	err := h.db.Pool.QueryRow(ctx, `
		SELECT id, name, url, type, xml_item_path, COALESCE(field_mapping::text,'{}')
		FROM feeds WHERE id=$1::uuid
	`, feedID).Scan(&feed.ID, &feed.Name, &feed.URL, &feed.Type, &feed.XMLItemPath, &fieldMappingStr)
	if err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "error": "Feed not found"})
	}
	json.Unmarshal([]byte(fieldMappingStr), &feed.FieldMapping)

	// Init progress
	progressMutex.Lock()
	importProgress[feedID] = &ImportProgress{
		FeedID:  feedID,
		Status:  "downloading",
		Message: "Sťahujem feed...",
		Logs:    []string{"Import started for: " + feed.Name},
	}
	progressMutex.Unlock()

	// Update feed status
	h.db.Pool.Exec(ctx, "UPDATE feeds SET last_status='running', last_run=NOW() WHERE id=$1::uuid", feedID)

	// Run import in background
	go h.runImport(feed)

	return c.JSON(fiber.Map{"success": true, "message": "Import started"})
}

func (h *Handlers) runImport(feed Feed) {
	ctx := context.Background()
	feedID := feed.ID

	updateProgress := func(status, message string) {
		progressMutex.Lock()
		if p, ok := importProgress[feedID]; ok {
			p.Status = status
			p.Message = message
			p.Logs = append(p.Logs, message)
			if p.Total > 0 {
				p.Percent = (p.Processed * 100) / p.Total
			}
		}
		progressMutex.Unlock()
	}

	// Download feed
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(feed.URL)
	if err != nil {
		updateProgress("failed", "Download failed: "+err.Error())
		h.db.Pool.Exec(ctx, "UPDATE feeds SET last_status='failed' WHERE id=$1::uuid", feedID)
		return
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	updateProgress("parsing", "Parsujem feed...")

	// Parse feed
	var items []map[string]interface{}
	switch feed.Type {
	case "xml":
		items = parseFullXML(data, feed.XMLItemPath)
	case "json":
		items = parseFullJSON(data)
	case "csv":
		items = parseFullCSV(data)
	}

	progressMutex.Lock()
	importProgress[feedID].Total = len(items)
	progressMutex.Unlock()

	updateProgress("importing", "Importujem "+string(rune(len(items)))+" produktov...")

	// Import products
	for i, item := range items {
		productData := mapFields(item, feed.FieldMapping)
		
		if productData["title"] == "" {
			progressMutex.Lock()
			importProgress[feedID].Skipped++
			importProgress[feedID].Processed++
			progressMutex.Unlock()
			continue
		}

		// Check if exists by EAN or SKU
		var existingID string
		if ean, ok := productData["ean"].(string); ok && ean != "" {
			h.db.Pool.QueryRow(ctx, "SELECT id FROM products WHERE ean=$1", ean).Scan(&existingID)
		}
		if existingID == "" {
			if sku, ok := productData["sku"].(string); ok && sku != "" {
				h.db.Pool.QueryRow(ctx, "SELECT id FROM products WHERE sku=$1", sku).Scan(&existingID)
			}
		}

		if existingID != "" {
			// Update existing
			h.updateProductFromFeed(ctx, existingID, productData)
			progressMutex.Lock()
			importProgress[feedID].Updated++
			progressMutex.Unlock()
		} else {
			// Create new
			newID := h.createProductFromFeed(ctx, productData, feedID)
			if newID != "" {
				progressMutex.Lock()
				importProgress[feedID].Created++
				progressMutex.Unlock()
			} else {
				progressMutex.Lock()
				importProgress[feedID].Errors++
				progressMutex.Unlock()
			}
		}

		progressMutex.Lock()
		importProgress[feedID].Processed++
		importProgress[feedID].Percent = ((i + 1) * 100) / len(items)
		progressMutex.Unlock()

		// Log every 100 items
		if (i+1)%100 == 0 {
			updateProgress("importing", "Spracované: "+string(rune(i+1))+"/"+string(rune(len(items))))
		}
	}

	// Complete
	progressMutex.Lock()
	p := importProgress[feedID]
	p.Status = "completed"
	p.Message = "Import dokončený"
	p.Percent = 100
	p.Logs = append(p.Logs, "Completed: "+string(rune(p.Created))+" created, "+string(rune(p.Updated))+" updated")
	progressMutex.Unlock()

	// Update feed stats
	h.db.Pool.Exec(ctx, `
		UPDATE feeds SET last_status='completed', product_count=$2 WHERE id=$1::uuid
	`, feedID, p.Created+p.Updated)

	// Sync to Elasticsearch
	h.syncFeedProductsToES(ctx, feedID)
}

func (h *Handlers) createProductFromFeed(ctx context.Context, data map[string]interface{}, feedID string) string {
	productID := uuid.New()
	title, _ := data["title"].(string)
	slug := makeSlug(title)
	description, _ := data["description"].(string)
	shortDesc, _ := data["short_description"].(string)
	ean, _ := data["ean"].(string)
	sku, _ := data["sku"].(string)
	brand, _ := data["brand"].(string)
	imageURL, _ := data["image_url"].(string)
	affiliateURL, _ := data["affiliate_url"].(string)
	
	var priceMin float64
	switch v := data["price"].(type) {
	case float64:
		priceMin = v
	case string:
		// Parse string to float
	}

	_, err := h.db.Pool.Exec(ctx, `
		INSERT INTO products (id, title, slug, description, short_description, ean, sku, brand, 
		                      image_url, affiliate_url, price_min, price_max, is_active, feed_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11, true, $12::uuid, NOW(), NOW())
	`, productID, title, slug, description, shortDesc, ean, sku, brand, imageURL, affiliateURL, priceMin, feedID)
	
	if err != nil {
		return ""
	}
	return productID.String()
}

func (h *Handlers) updateProductFromFeed(ctx context.Context, productID string, data map[string]interface{}) {
	title, _ := data["title"].(string)
	description, _ := data["description"].(string)
	imageURL, _ := data["image_url"].(string)
	
	var priceMin float64
	switch v := data["price"].(type) {
	case float64:
		priceMin = v
	}

	h.db.Pool.Exec(ctx, `
		UPDATE products SET title=COALESCE(NULLIF($2,''),title), description=COALESCE(NULLIF($3,''),description),
		       image_url=COALESCE(NULLIF($4,''),image_url), price_min=$5, price_max=$5, updated_at=NOW()
		WHERE id=$1::uuid
	`, productID, title, description, imageURL, priceMin)
}

func (h *Handlers) syncFeedProductsToES(ctx context.Context, feedID string) {
	rows, _ := h.db.Pool.Query(ctx, `
		SELECT p.id, p.title, p.slug, COALESCE(p.description,''), COALESCE(p.short_description,''),
		       COALESCE(p.ean,''), COALESCE(p.sku,''), COALESCE(p.brand,''),
		       COALESCE(p.category_id::text,''), COALESCE(c.name,''), COALESCE(c.slug,''),
		       COALESCE(p.image_url,''), p.price_min, p.price_max, COALESCE(p.stock_status,'instock'),
		       p.is_active, COALESCE(p.is_featured,false), p.created_at
		FROM products p LEFT JOIN categories c ON p.category_id=c.id
		WHERE p.feed_id=$1::uuid
	`, feedID)
	if rows == nil {
		return
	}
	defer rows.Close()

	var products []elasticsearch.Product
	for rows.Next() {
		var p elasticsearch.Product
		var createdAt time.Time
		rows.Scan(&p.ID, &p.Title, &p.Slug, &p.Description, &p.ShortDescription,
			&p.EAN, &p.SKU, &p.Brand, &p.CategoryID, &p.CategoryName, &p.CategorySlug,
			&p.ImageURL, &p.PriceMin, &p.PriceMax, &p.StockStatus, &p.IsActive, &p.IsFeatured, &createdAt)
		p.CreatedAt = createdAt.Format(time.RFC3339)
		products = append(products, p)
	}

	if len(products) > 0 {
		h.es.BulkIndex(products)
		h.es.Refresh()
	}
}

func mapFields(item map[string]interface{}, mapping map[string]string) map[string]interface{} {
	result := make(map[string]interface{})
	for sourceField, targetField := range mapping {
		if targetField != "" {
			if val, ok := item[sourceField]; ok {
				result[targetField] = val
			}
		}
	}
	// Also try auto-mapping common fields
	autoMap := map[string][]string{
		"title":       {"PRODUCTNAME", "PRODUCT", "NAME", "NAZOV", "TITLE"},
		"description": {"DESCRIPTION", "POPIS", "DESC"},
		"price":       {"PRICE_VAT", "PRICE", "CENA"},
		"ean":         {"EAN", "EAN13", "GTIN", "BARCODE"},
		"sku":         {"SKU", "ITEM_ID", "PRODUCTNO", "KOD"},
		"brand":       {"MANUFACTURER", "BRAND", "VYROBCE", "ZNACKA"},
		"image_url":   {"IMGURL", "IMG_URL", "IMAGE", "OBRAZOK"},
		"affiliate_url": {"URL", "ITEM_URL", "PRODUCT_URL"},
	}
	for target, sources := range autoMap {
		if result[target] == nil {
			for _, src := range sources {
				if val, ok := item[src]; ok && val != "" {
					result[target] = val
					break
				}
			}
		}
	}
	return result
}

func parseFullXML(data []byte, itemPath string) []map[string]interface{} {
	var items []map[string]interface{}
	if itemPath == "" {
		itemPath = "SHOPITEM"
	}

	decoder := xml.NewDecoder(bytes.NewReader(data))
	var currentItem map[string]interface{}
	var currentElement string
	var inItem bool

	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch t := token.(type) {
		case xml.StartElement:
			if strings.EqualFold(t.Name.Local, itemPath) {
				inItem = true
				currentItem = make(map[string]interface{})
			} else if inItem {
				currentElement = t.Name.Local
			}
		case xml.CharData:
			if inItem && currentElement != "" {
				value := strings.TrimSpace(string(t))
				if value != "" {
					currentItem[currentElement] = value
				}
			}
		case xml.EndElement:
			if strings.EqualFold(t.Name.Local, itemPath) {
				inItem = false
				if len(currentItem) > 0 {
					items = append(items, currentItem)
				}
				currentItem = nil
			}
			currentElement = ""
		}
	}
	return items
}

func parseFullJSON(data []byte) []map[string]interface{} {
	var items []map[string]interface{}
	var jsonData interface{}
	if err := json.Unmarshal(data, &jsonData); err != nil {
		return items
	}

	switch v := jsonData.(type) {
	case []interface{}:
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				items = append(items, m)
			}
		}
	case map[string]interface{}:
		for _, key := range []string{"products", "items", "data", "results", "offers"} {
			if arr, ok := v[key].([]interface{}); ok {
				for _, item := range arr {
					if m, ok := item.(map[string]interface{}); ok {
						items = append(items, m)
					}
				}
				break
			}
		}
	}
	return items
}

func parseFullCSV(data []byte) []map[string]interface{} {
	var items []map[string]interface{}
	firstLine := strings.Split(string(data), "\n")[0]
	delimiter := ';'
	if strings.Count(firstLine, ",") > strings.Count(firstLine, ";") {
		delimiter = ','
	}

	reader := csv.NewReader(bytes.NewReader(data))
	reader.Comma = delimiter
	reader.LazyQuotes = true

	header, err := reader.Read()
	if err != nil {
		return items
	}

	for {
		row, err := reader.Read()
		if err != nil {
			break
		}
		item := make(map[string]interface{})
		for j, val := range row {
			if j < len(header) {
				item[header[j]] = val
			}
		}
		items = append(items, item)
	}
	return items
}

func (h *Handlers) GetImportProgress(c *fiber.Ctx) error {
	feedID := c.Params("id")
	progressMutex.RLock()
	progress, ok := importProgress[feedID]
	progressMutex.RUnlock()

	if !ok {
		return c.JSON(fiber.Map{"success": true, "data": fiber.Map{"status": "idle"}})
	}
	return c.JSON(fiber.Map{"success": true, "data": progress})
}
