package handlers

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"megabuy-go/internal/elasticsearch"
)

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
	Attributes   []AttributePreview       `json:"attributes,omitempty"`
	Categories   []CategoryPreview        `json:"categories,omitempty"`
}

type AttributePreview struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type CategoryPreview struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
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

	data, err := downloadFeedData(input.URL, 2*1024*1024) // 2MB for preview
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "Cannot download feed: " + err.Error()})
	}

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

	itemPath := input.XMLItemPath
	if itemPath == "" {
		itemPath = "SHOPITEM"
	}

	var preview FeedPreview
	switch detectedType {
	case "xml":
		preview = parseXMLPreviewWithAttributes(data, itemPath)
	case "json":
		preview = parseJSONPreview(data)
	case "csv":
		preview = parseCSVPreview(data)
	}
	preview.DetectedType = detectedType

	return c.JSON(fiber.Map{"success": true, "data": preview})
}

func (h *Handlers) StartImport(c *fiber.Ctx) error {
	feedID := c.Params("id")
	ctx := context.Background()

	var feed Feed
	var fieldMappingStr string
	err := h.db.Pool.QueryRow(ctx, `
		SELECT id, name, url, type, COALESCE(xml_item_path,'SHOPITEM'), COALESCE(field_mapping::text,'{}')
		FROM feeds WHERE id=$1::uuid
	`, feedID).Scan(&feed.ID, &feed.Name, &feed.URL, &feed.Type, &feed.XMLItemPath, &fieldMappingStr)
	if err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "error": "Feed not found"})
	}
	json.Unmarshal([]byte(fieldMappingStr), &feed.FieldMapping)

	progressMutex.Lock()
	importProgress[feedID] = &ImportProgress{
		FeedID:  feedID,
		Status:  "downloading",
		Message: "Stahujem feed...",
		Logs:    []string{"Import started for: " + feed.Name},
	}
	progressMutex.Unlock()

	h.db.Pool.Exec(ctx, "UPDATE feeds SET last_status='running', last_run=NOW() WHERE id=$1::uuid", feedID)

	go h.runImport(feed)

	return c.JSON(fiber.Map{"success": true, "message": "Import started"})
}

func downloadFeedData(url string, maxBytes int) ([]byte, error) {
	if strings.HasPrefix(url, "/") {
		data, err := os.ReadFile(url)
		if err != nil {
			return nil, err
		}
		if maxBytes > 0 && len(data) > maxBytes {
			return data[:maxBytes], nil
		}
		return data, nil
	}

	tr := &http.Transport{
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
		DisableCompression:    false,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 120 * time.Second,
	}
	client := &http.Client{
		Timeout:   15 * time.Minute,
		Transport: tr,
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "*/*")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	if maxBytes > 0 {
		data := make([]byte, maxBytes)
		n, _ := io.ReadFull(resp.Body, data)
		return data[:n], nil
	}

	return io.ReadAll(resp.Body)
}

func (h *Handlers) runImport(feed Feed) {
	ctx := context.Background()
	feedID := feed.ID

	defer func() {
		if r := recover(); r != nil {
			progressMutex.Lock()
			if p, ok := importProgress[feedID]; ok {
				p.Status = "failed"
				p.Message = fmt.Sprintf("Panic: %v", r)
				p.Logs = append(p.Logs, fmt.Sprintf("Error: %v", r))
			}
			progressMutex.Unlock()
			h.db.Pool.Exec(ctx, "UPDATE feeds SET last_status='failed' WHERE id=$1::uuid", feedID)
		}
	}()

	addLog := func(msg string) {
		progressMutex.Lock()
		if p, ok := importProgress[feedID]; ok {
			p.Logs = append(p.Logs, msg)
			if len(p.Logs) > 100 {
				p.Logs = p.Logs[len(p.Logs)-100:]
			}
		}
		progressMutex.Unlock()
	}

	updateStatus := func(status, message string) {
		progressMutex.Lock()
		if p, ok := importProgress[feedID]; ok {
			p.Status = status
			p.Message = message
		}
		progressMutex.Unlock()
	}

	addLog("Downloading from: " + feed.URL)
	data, err := downloadFeedData(feed.URL, 0)
	if err != nil {
		addLog("Download failed: " + err.Error())
		updateStatus("failed", "Download failed: "+err.Error())
		h.db.Pool.Exec(ctx, "UPDATE feeds SET last_status='failed' WHERE id=$1::uuid", feedID)
		return
	}
	addLog(fmt.Sprintf("Downloaded %d KB", len(data)/1024))

	updateStatus("parsing", "Parsujem feed...")

	var items []map[string]interface{}
	switch feed.Type {
	case "xml":
		items = parseFullXMLWithParams(data, feed.XMLItemPath)
	case "json":
		items = parseFullJSON(data)
	case "csv":
		items = parseFullCSV(data)
	}

	addLog(fmt.Sprintf("Parsed %d items", len(items)))

	if len(items) == 0 {
		addLog("No items found in feed")
		updateStatus("failed", "Feed neobsahuje produkty")
		h.db.Pool.Exec(ctx, "UPDATE feeds SET last_status='failed' WHERE id=$1::uuid", feedID)
		return
	}

	progressMutex.Lock()
	importProgress[feedID].Total = len(items)
	progressMutex.Unlock()

	updateStatus("importing", fmt.Sprintf("Importujem %d produktov...", len(items)))

	created, updated, skipped, errors := 0, 0, 0, 0

	for i, item := range items {
		productData := mapFields(item, feed.FieldMapping)

		title := getStr(productData, "title")
		if title == "" {
			skipped++
			continue
		}

		price := getFloat(productData, "price")
		if price <= 0 {
			skipped++
			continue
		}

		var existingID string
		ean := getStr(productData, "ean")
		sku := getStr(productData, "sku")

		if ean != "" {
			h.db.Pool.QueryRow(ctx, "SELECT id FROM products WHERE ean=$1", ean).Scan(&existingID)
		}
		if existingID == "" && sku != "" {
			h.db.Pool.QueryRow(ctx, "SELECT id FROM products WHERE sku=$1", sku).Scan(&existingID)
		}

		// Get PARAM attributes from item
		params := getParams(item)

		if existingID != "" {
			err := h.updateProductFromFeed(ctx, existingID, productData, params)
			if err == nil {
				updated++
			} else {
				errors++
				addLog(fmt.Sprintf("Update error: %v", err))
			}
		} else {
			newID := h.createProductFromFeed(ctx, productData, feedID, params)
			if newID != "" {
				created++
			} else {
				errors++
			}
		}

		if (i+1)%50 == 0 || i == len(items)-1 {
			progressMutex.Lock()
			if p, ok := importProgress[feedID]; ok {
				p.Processed = i + 1
				p.Created = created
				p.Updated = updated
				p.Skipped = skipped
				p.Errors = errors
				p.Percent = ((i + 1) * 100) / len(items)
				p.Message = fmt.Sprintf("Spracovane %d/%d", i+1, len(items))
			}
			progressMutex.Unlock()
		}

		if (i+1)%500 == 0 {
			addLog(fmt.Sprintf("Progress: %d/%d (created: %d, updated: %d)", i+1, len(items), created, updated))
		}
	}

	addLog(fmt.Sprintf("Completed: %d created, %d updated, %d skipped, %d errors", created, updated, skipped, errors))
	updateStatus("completed", fmt.Sprintf("Hotovo: %d vytvorenych, %d aktualizovanych", created, updated))

	progressMutex.Lock()
	if p, ok := importProgress[feedID]; ok {
		p.Percent = 100
		p.Processed = len(items)
		p.Created = created
		p.Updated = updated
		p.Skipped = skipped
		p.Errors = errors
	}
	progressMutex.Unlock()

	h.db.Pool.Exec(ctx, "UPDATE feeds SET last_status='completed', product_count=$2 WHERE id=$1::uuid", feedID, created+updated)

	// Update category counts
	h.db.Pool.Exec(ctx, `UPDATE categories SET product_count = (SELECT COUNT(*) FROM products WHERE category_id = categories.id AND is_active = true)`)

	// Sync to Elasticsearch
	addLog("Syncing to Elasticsearch...")
	h.syncFeedProductsToES(ctx, feedID)
	addLog("Elasticsearch sync completed")
}

// getParams extracts PARAM attributes from parsed item
func getParams(item map[string]interface{}) []map[string]string {
	var params []map[string]string
	if p, ok := item["_params"]; ok {
		if paramList, ok := p.([]map[string]string); ok {
			params = paramList
		}
	}
	return params
}

func (h *Handlers) createProductFromFeed(ctx context.Context, data map[string]interface{}, feedID string, params []map[string]string) string {
	productID := uuid.New()
	title := getStr(data, "title")
	slug := makeSlug(title)
	description := getStr(data, "description")
	shortDesc := getStr(data, "short_description")
	ean := getStr(data, "ean")
	sku := getStr(data, "sku")
	brand := getStr(data, "brand")
	imageURL := getStr(data, "image_url")
	affiliateURL := getStr(data, "affiliate_url")
	category := getStr(data, "category")
	price := getFloat(data, "price")

	var categoryID *string
	if category != "" {
		catID := h.findOrCreateCategoryFeed(ctx, category)
		if catID != "" {
			categoryID = &catID
		}
	}

	_, err := h.db.Pool.Exec(ctx, `
		INSERT INTO products (id, title, slug, description, short_description, ean, sku, brand, 
		                      image_url, affiliate_url, category_id, price_min, price_max, stock_status, is_active, feed_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12, 'instock', true, $13::uuid, NOW(), NOW())
	`, productID, title, slug, description, shortDesc, ean, sku, brand, imageURL, affiliateURL, categoryID, price, feedID)

	if err != nil {
		return ""
	}

	// Save PARAM attributes
	h.saveProductAttributes(ctx, productID.String(), params)

	if categoryID != nil {
		h.db.Pool.Exec(ctx, "UPDATE categories SET product_count = product_count + 1 WHERE id = $1::uuid", *categoryID)
	}

	return productID.String()
}

func (h *Handlers) updateProductFromFeed(ctx context.Context, productID string, data map[string]interface{}, params []map[string]string) error {
	title := getStr(data, "title")
	description := getStr(data, "description")
	imageURL := getStr(data, "image_url")
	price := getFloat(data, "price")

	_, err := h.db.Pool.Exec(ctx, `
		UPDATE products SET title=COALESCE(NULLIF($2,''),title), description=COALESCE(NULLIF($3,''),description),
		       image_url=COALESCE(NULLIF($4,''),image_url), price_min=$5, price_max=$5, updated_at=NOW()
		WHERE id=$1::uuid
	`, productID, title, description, imageURL, price)

	if err == nil {
		// Update PARAM attributes
		h.saveProductAttributes(ctx, productID, params)
	}

	return err
}

// saveProductAttributes saves PARAM tags to product_attributes table
func (h *Handlers) saveProductAttributes(ctx context.Context, productID string, params []map[string]string) {
	if len(params) == 0 {
		return
	}

	// Delete existing attributes for this product
	h.db.Pool.Exec(ctx, "DELETE FROM product_attributes WHERE product_id = $1::uuid", productID)

	// Insert new attributes - using existing table structure (name, value, position)
	for i, param := range params {
		name := param["name"]
		value := param["value"]
		if name != "" && value != "" {
			h.db.Pool.Exec(ctx, `
				INSERT INTO product_attributes (id, product_id, name, value, position, created_at)
				VALUES ($1::uuid, $2::uuid, $3, $4, $5, NOW())
			`, uuid.New().String(), productID, name, value, i)
		}
	}
}

func (h *Handlers) findOrCreateCategoryFeed(ctx context.Context, categoryText string) string {
	parts := strings.Split(categoryText, " | ")
	if len(parts) == 1 {
		parts = strings.Split(categoryText, "|")
	}
	if len(parts) == 1 {
		parts = strings.Split(categoryText, " > ")
	}
	if len(parts) == 1 {
		parts = strings.Split(categoryText, ">")
	}

	var parentID *string
	var lastID string

	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		slug := makeSlug(name)

		var catID string
		if parentID != nil {
			h.db.Pool.QueryRow(ctx, "SELECT id FROM categories WHERE slug = $1 AND parent_id = $2::uuid", slug, *parentID).Scan(&catID)
		} else {
			h.db.Pool.QueryRow(ctx, "SELECT id FROM categories WHERE slug = $1 AND parent_id IS NULL", slug).Scan(&catID)
		}

		if catID == "" {
			catID = uuid.New().String()
			if parentID != nil {
				h.db.Pool.Exec(ctx, "INSERT INTO categories (id, parent_id, name, slug, is_active, created_at, updated_at) VALUES ($1::uuid, $2::uuid, $3, $4, true, NOW(), NOW())", catID, *parentID, name, slug)
			} else {
				h.db.Pool.Exec(ctx, "INSERT INTO categories (id, name, slug, is_active, created_at, updated_at) VALUES ($1::uuid, $2, $3, true, NOW(), NOW())", catID, name, slug)
			}
		}

		lastID = catID
		parentID = &catID
	}

	return lastID
}

func (h *Handlers) syncFeedProductsToES(ctx context.Context, feedID string) {
	if h.es == nil {
		return
	}

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
		if targetField != "" && targetField != "--" && targetField != "-- Ignorovat --" {
			if val, ok := item[sourceField]; ok && val != nil && val != "" {
				result[targetField] = val
			}
		}
	}

	autoMap := map[string][]string{
		"title":             {"PRODUCTNAME", "PRODUCT", "NAME", "NAZOV", "TITLE", "title", "name", "product_name"},
		"description":       {"DESCRIPTION", "POPIS", "DESC", "description", "long_description"},
		"short_description": {"SHORT_DESCRIPTION", "SHORT_DESC", "KRATKY_POPIS"},
		"price":             {"PRICE_VAT", "PRICE", "CENA", "price", "price_vat", "cena_s_dph"},
		"ean":               {"EAN", "EAN13", "GTIN", "BARCODE", "ean", "gtin", "barcode"},
		"sku":               {"SKU", "ITEM_ID", "PRODUCTNO", "KOD", "sku", "item_id", "product_id", "PRODUCT_ID"},
		"brand":             {"MANUFACTURER", "BRAND", "VYROBCE", "ZNACKA", "brand", "manufacturer", "znacka"},
		"image_url":         {"IMGURL", "IMG_URL", "IMAGE", "OBRAZOK", "image_url", "imgurl", "image", "img"},
		"affiliate_url":     {"URL", "ITEM_URL", "PRODUCT_URL", "url", "product_url", "link"},
		"category":          {"CATEGORYTEXT", "CATEGORY", "KATEGORIA", "category", "kategorie", "category_text"},
	}

	for target, sources := range autoMap {
		if result[target] == nil || result[target] == "" {
			for _, src := range sources {
				if val, ok := item[src]; ok && val != nil && val != "" {
					result[target] = val
					break
				}
			}
		}
	}

	return result
}

func getStr(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		switch s := v.(type) {
		case string:
			return strings.TrimSpace(s)
		default:
			return fmt.Sprintf("%v", v)
		}
	}
	return ""
}

func getFloat(m map[string]interface{}, key string) float64 {
	if v, ok := m[key]; ok {
		switch f := v.(type) {
		case float64:
			return f
		case int:
			return float64(f)
		case int64:
			return float64(f)
		case string:
			s := strings.ReplaceAll(f, ",", ".")
			s = strings.TrimSpace(s)
			re := regexp.MustCompile(`[^\d.]`)
			s = re.ReplaceAllString(s, "")
			if val, err := strconv.ParseFloat(s, 64); err == nil {
				return val
			}
		}
	}
	return 0
}

// ========== XML PARSING WITH PARAM SUPPORT ==========

// parseFullXMLWithParams parses XML and extracts PARAM tags
func parseFullXMLWithParams(data []byte, itemPath string) []map[string]interface{} {
	if itemPath == "" {
		itemPath = "SHOPITEM"
	}

	var items []map[string]interface{}
	content := string(data)

	pattern := fmt.Sprintf(`(?s)<%s[^>]*>(.*?)</%s>`, itemPath, itemPath)
	re := regexp.MustCompile(pattern)
	matches := re.FindAllStringSubmatch(content, -1)

	for _, match := range matches {
		if len(match) > 1 {
			item := parseXMLItemWithParams(match[1])
			if len(item) > 0 {
				items = append(items, item)
			}
		}
	}

	return items
}

// parseXMLItemWithParams extracts fields AND PARAM tags from XML item
func parseXMLItemWithParams(xmlStr string) map[string]interface{} {
	result := make(map[string]interface{})

	tags := []string{
		"PRODUCTNAME", "PRODUCT", "DESCRIPTION", "PRICE_VAT", "PRICE",
		"EAN", "ITEM_ID", "SKU", "MANUFACTURER", "BRAND",
		"IMGURL", "URL", "CATEGORYTEXT", "CATEGORY",
		"DELIVERY_DATE", "ITEM_TYPE", "PRODUCT_ID", "NAME",
		"SHORT_DESCRIPTION", "PRODUCTNO",
	}

	for _, tag := range tags {
		value := extractXMLTag(xmlStr, tag)
		if value != "" {
			result[tag] = value
		}
	}

	// Extract PARAM tags - THIS IS THE KEY PART!
	params := extractParams(xmlStr)
	if len(params) > 0 {
		result["_params"] = params
	}

	return result
}

// extractXMLTag extracts value from XML tag (handles CDATA)
func extractXMLTag(xmlStr, tag string) string {
	// Try CDATA first
	cdataPattern := fmt.Sprintf(`<%s[^>]*><!\[CDATA\[(.*?)\]\]></%s>`, tag, tag)
	re := regexp.MustCompile(cdataPattern)
	match := re.FindStringSubmatch(xmlStr)
	if len(match) > 1 {
		return strings.TrimSpace(match[1])
	}

	// Try regular content
	pattern := fmt.Sprintf(`<%s[^>]*>([^<]*)</%s>`, tag, tag)
	re = regexp.MustCompile(pattern)
	match = re.FindStringSubmatch(xmlStr)
	if len(match) > 1 {
		return strings.TrimSpace(match[1])
	}

	return ""
}

// extractParams extracts all PARAM tags from XML
func extractParams(xmlStr string) []map[string]string {
	var params []map[string]string

	// Pattern for PARAM blocks
	paramPattern := `(?s)<PARAM>(.*?)</PARAM>`
	re := regexp.MustCompile(paramPattern)
	matches := re.FindAllStringSubmatch(xmlStr, -1)

	for _, match := range matches {
		if len(match) > 1 {
			paramContent := match[1]

			// Extract PARAM_NAME
			name := extractXMLTag(paramContent, "PARAM_NAME")
			if name == "" {
				name = extractXMLTag(paramContent, "NAME")
			}

			// Extract VAL (value)
			value := extractXMLTag(paramContent, "VAL")
			if value == "" {
				value = extractXMLTag(paramContent, "VALUE")
			}

			if name != "" && value != "" {
				params = append(params, map[string]string{
					"name":  name,
					"value": value,
				})
			}
		}
	}

	return params
}

// parseXMLPreviewWithAttributes parses XML for preview including attributes stats
func parseXMLPreviewWithAttributes(data []byte, itemPath string) FeedPreview {
	items := parseFullXMLWithParams(data, itemPath)
	totalItems := len(items)

	// Collect attribute statistics
	attrCounts := make(map[string]int)
	catCounts := make(map[string]int)

	for _, item := range items {
		// Count attributes
		if params, ok := item["_params"].([]map[string]string); ok {
			for _, p := range params {
				if name := p["name"]; name != "" {
					attrCounts[name]++
				}
			}
		}

		// Count categories
		if cat, ok := item["CATEGORYTEXT"].(string); ok && cat != "" {
			catCounts[cat]++
		} else if cat, ok := item["CATEGORY"].(string); ok && cat != "" {
			catCounts[cat]++
		}
	}

	// Convert to slices
	var attributes []AttributePreview
	for name, count := range attrCounts {
		attributes = append(attributes, AttributePreview{Name: name, Count: count})
	}

	var categories []CategoryPreview
	for name, count := range catCounts {
		categories = append(categories, CategoryPreview{Name: name, Count: count})
	}

	// Get sample items (without _params for cleaner display)
	sampleItems := items
	if len(sampleItems) > 5 {
		sampleItems = sampleItems[:5]
	}

	// Clean samples - show params separately
	var cleanSamples []map[string]interface{}
	for _, item := range sampleItems {
		cleanItem := make(map[string]interface{})
		for k, v := range item {
			if k != "_params" {
				cleanItem[k] = v
			}
		}
		// Add param count
		if params, ok := item["_params"].([]map[string]string); ok {
			cleanItem["_param_count"] = len(params)
			// Show first 3 params as preview
			if len(params) > 0 {
				preview := []string{}
				for i, p := range params {
					if i >= 3 {
						break
					}
					preview = append(preview, fmt.Sprintf("%s: %s", p["name"], p["value"]))
				}
				cleanItem["_params_preview"] = preview
			}
		}
		cleanSamples = append(cleanSamples, cleanItem)
	}

	// Collect all unique fields
	fieldsMap := make(map[string]bool)
	for _, item := range sampleItems {
		for k := range item {
			if k != "_params" {
				fieldsMap[k] = true
			}
		}
	}

	fields := make([]string, 0, len(fieldsMap))
	for k := range fieldsMap {
		fields = append(fields, k)
	}

	if cleanSamples == nil {
		cleanSamples = []map[string]interface{}{}
	}
	if fields == nil {
		fields = []string{}
	}
	if attributes == nil {
		attributes = []AttributePreview{}
	}
	if categories == nil {
		categories = []CategoryPreview{}
	}

	return FeedPreview{
		Fields:     fields,
		Sample:     cleanSamples,
		TotalItems: totalItems,
		Attributes: attributes,
		Categories: categories,
	}
}

func parseJSONPreview(data []byte) FeedPreview {
	items := parseFullJSON(data)
	totalItems := len(items)
	if len(items) > 5 {
		items = items[:5]
	}
	fields := []string{}
	if len(items) > 0 {
		for k := range items[0] {
			fields = append(fields, k)
		}
	}
	if items == nil {
		items = []map[string]interface{}{}
	}
	return FeedPreview{Fields: fields, Sample: items, TotalItems: totalItems}
}

func parseCSVPreview(data []byte) FeedPreview {
	items := parseFullCSV(data)
	totalItems := len(items)
	if len(items) > 5 {
		items = items[:5]
	}
	fields := []string{}
	if len(items) > 0 {
		for k := range items[0] {
			fields = append(fields, k)
		}
	}
	if items == nil {
		items = []map[string]interface{}{}
	}
	return FeedPreview{Fields: fields, Sample: items, TotalItems: totalItems}
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

	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 {
		return items
	}

	firstLine := lines[0]
	delimiter := ';'
	if strings.Count(firstLine, ",") > strings.Count(firstLine, ";") {
		delimiter = ','
	}
	if strings.Count(firstLine, "\t") > strings.Count(firstLine, string(delimiter)) {
		delimiter = '\t'
	}

	reader := csv.NewReader(bytes.NewReader(data))
	reader.Comma = delimiter
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1

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
				item[header[j]] = strings.TrimSpace(val)
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
