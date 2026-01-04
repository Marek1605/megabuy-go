package handlers

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"megabuy-go/internal/database"
	"megabuy-go/internal/elasticsearch"
)

type Handlers struct {
	db             *database.DB
	es             *elasticsearch.Client
	importProgress sync.Map
}

type ImportProgress struct {
	FeedID    string `json:"feed_id"`
	Status    string `json:"status"`
	Total     int    `json:"total"`
	Processed int    `json:"processed"`
	Success   int    `json:"success"`
	Failed    int    `json:"failed"`
	StartedAt string `json:"started_at"`
	Message   string `json:"message"`
}

func New(db *database.DB) *Handlers {
	es := elasticsearch.New()
	if es != nil {
		es.CreateIndex()
	}
	return &Handlers{db: db, es: es}
}

func makeSlug(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	r, _, _ := transform.String(t, strings.ToLower(s))
	result := strings.Map(func(c rune) rune {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			return c
		}
		if c == ' ' || c == '-' {
			return '-'
		}
		return -1
	}, r)
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	return strings.Trim(result, "-")
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

// ========== SEARCH API (Elasticsearch) ==========

func (h *Handlers) Search(c *fiber.Ctx) error {
	if h.es == nil {
		return c.Status(503).JSON(fiber.Map{"success": false, "error": "Elasticsearch not available"})
	}

	params := elasticsearch.SearchParams{
		Query:      c.Query("q"),
		CategoryID: c.Query("category_id"),
		Brand:      c.Query("brand"),
		PriceMin:   float64(c.QueryInt("price_min", 0)),
		PriceMax:   float64(c.QueryInt("price_max", 0)),
		InStock:    c.Query("in_stock") == "true",
		Sort:       c.Query("sort", "relevance"),
		Page:       c.QueryInt("page", 1),
		Limit:      c.QueryInt("limit", 20),
	}

	result, err := h.es.Search(c.Context(), params)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error()})
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data": fiber.Map{
			"items":       result.Products,
			"total":       result.Total,
			"page":        params.Page,
			"limit":       params.Limit,
			"total_pages": (result.Total + int64(params.Limit) - 1) / int64(params.Limit),
			"facets":      result.Facets,
			"took_ms":     result.Took,
		},
	})
}

func (h *Handlers) SyncToElasticsearch(c *fiber.Ctx) error {
	if h.es == nil {
		return c.Status(503).JSON(fiber.Map{"success": false, "error": "Elasticsearch not configured"})
	}

	ctx := context.Background()

	rows, err := h.db.Pool.Query(ctx, `
		SELECT p.id, p.title, p.slug, COALESCE(p.description,''), COALESCE(p.short_description,''),
		       COALESCE(p.ean,''), COALESCE(p.sku,''), COALESCE(p.brand,''),
		       COALESCE(p.category_id::text,''), COALESCE(c.name,''), COALESCE(c.slug,''),
		       COALESCE(p.image_url,''), p.price_min, p.price_max, COALESCE(p.stock_status,'instock'),
		       p.is_active, COALESCE(p.is_featured, false), p.created_at
		FROM products p
		LEFT JOIN categories c ON p.category_id = c.id
	`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error()})
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

	batchSize := 1000
	indexed := 0
	for i := 0; i < len(products); i += batchSize {
		end := i + batchSize
		if end > len(products) {
			end = len(products)
		}
		if err := h.es.BulkIndex(products[i:end]); err != nil {
			return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error(), "indexed": indexed})
		}
		indexed += end - i
	}

	h.es.Refresh()

	return c.JSON(fiber.Map{
		"success": true,
		"message": fmt.Sprintf("Synced %d products to Elasticsearch", indexed),
		"count":   indexed,
	})
}

// ========== PUBLIC API ==========

func (h *Handlers) GetProducts(c *fiber.Ctx) error {
	page := c.QueryInt("page", 1)
	limit := c.QueryInt("limit", 20)
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * limit
	ctx := context.Background()

	whereClause := "WHERE p.is_active=true"
	args := []interface{}{}
	argNum := 1

	if cat := c.Query("category"); cat != "" {
		whereClause += fmt.Sprintf(" AND c.slug = $%d", argNum)
		args = append(args, cat)
		argNum++
	}

	if brand := c.Query("brand"); brand != "" {
		brands := strings.Split(brand, ",")
		placeholders := []string{}
		for _, b := range brands {
			placeholders = append(placeholders, fmt.Sprintf("$%d", argNum))
			args = append(args, b)
			argNum++
		}
		whereClause += fmt.Sprintf(" AND p.brand IN (%s)", strings.Join(placeholders, ","))
	}

	if minPrice := c.QueryInt("min_price", 0); minPrice > 0 {
		whereClause += fmt.Sprintf(" AND p.price_min >= $%d", argNum)
		args = append(args, minPrice)
		argNum++
	}
	if maxPrice := c.QueryInt("max_price", 0); maxPrice > 0 {
		whereClause += fmt.Sprintf(" AND p.price_min <= $%d", argNum)
		args = append(args, maxPrice)
		argNum++
	}

	if c.Query("in_stock") == "true" {
		whereClause += " AND p.stock_status = 'instock'"
	}

	var total int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM products p LEFT JOIN categories c ON p.category_id = c.id %s", whereClause)
	h.db.Pool.QueryRow(ctx, countQuery, args...).Scan(&total)

	orderBy := "ORDER BY p.created_at DESC"
	switch c.Query("sort") {
	case "price_asc":
		orderBy = "ORDER BY p.price_min ASC"
	case "price_desc":
		orderBy = "ORDER BY p.price_min DESC"
	case "name_asc":
		orderBy = "ORDER BY p.title ASC"
	case "newest":
		orderBy = "ORDER BY p.created_at DESC"
	}

	args = append(args, limit, offset)
	query := fmt.Sprintf(`
		SELECT p.id, p.title, p.slug, COALESCE(p.short_description,''), COALESCE(p.image_url,''), 
		       p.price_min, p.price_max, COALESCE(p.stock_status,'instock'), COALESCE(p.brand,''),
		       COALESCE(c.name,''), COALESCE(c.slug,'')
		FROM products p LEFT JOIN categories c ON p.category_id = c.id
		%s %s LIMIT $%d OFFSET $%d
	`, whereClause, orderBy, argNum, argNum+1)

	rows, _ := h.db.Pool.Query(ctx, query, args...)
	defer rows.Close()

	var products []fiber.Map
	for rows.Next() {
		var id, title, slug, shortDesc, img, stockStatus, brand, catName, catSlug string
		var pmin, pmax float64
		rows.Scan(&id, &title, &slug, &shortDesc, &img, &pmin, &pmax, &stockStatus, &brand, &catName, &catSlug)
		products = append(products, fiber.Map{
			"id": id, "title": title, "slug": slug, "short_description": shortDesc,
			"image_url": img, "price_min": pmin, "price_max": pmax, "stock_status": stockStatus,
			"brand": brand, "category_name": catName, "category_slug": catSlug,
		})
	}
	if products == nil {
		products = []fiber.Map{}
	}

	facets := h.getProductFacets(ctx, whereClause, args[:len(args)-2])

	return c.JSON(fiber.Map{"success": true, "data": fiber.Map{
		"items": products, "total": total, "page": page, "limit": limit,
		"total_pages": (total + limit - 1) / limit,
		"facets":      facets,
	}})
}

func (h *Handlers) getProductFacets(ctx context.Context, whereClause string, args []interface{}) fiber.Map {
	brandQuery := fmt.Sprintf(`
		SELECT p.brand, COUNT(*) as cnt FROM products p 
		LEFT JOIN categories c ON p.category_id = c.id
		%s AND p.brand != '' GROUP BY p.brand ORDER BY cnt DESC LIMIT 50
	`, whereClause)
	brandRows, _ := h.db.Pool.Query(ctx, brandQuery, args...)
	defer brandRows.Close()

	var brands []fiber.Map
	for brandRows.Next() {
		var name string
		var count int
		brandRows.Scan(&name, &count)
		brands = append(brands, fiber.Map{"name": name, "count": count})
	}

	priceQuery := fmt.Sprintf(`
		SELECT MIN(p.price_min), MAX(p.price_min) FROM products p 
		LEFT JOIN categories c ON p.category_id = c.id %s
	`, whereClause)
	var minPrice, maxPrice float64
	h.db.Pool.QueryRow(ctx, priceQuery, args...).Scan(&minPrice, &maxPrice)

	return fiber.Map{
		"brands":      brands,
		"price_range": fiber.Map{"min": minPrice, "max": maxPrice},
	}
}

func (h *Handlers) GetFeaturedProducts(c *fiber.Ctx) error {
	limit := c.QueryInt("limit", 8)
	ctx := context.Background()
	rows, _ := h.db.Pool.Query(ctx, `
		SELECT p.id, p.title, p.slug, COALESCE(p.image_url,''), p.price_min, p.price_max, COALESCE(p.brand,''), COALESCE(c.name,''), COALESCE(c.slug,'')
		FROM products p LEFT JOIN categories c ON p.category_id = c.id
		WHERE p.is_active=true ORDER BY p.is_featured DESC, p.created_at DESC LIMIT $1
	`, limit)
	defer rows.Close()
	var products []fiber.Map
	for rows.Next() {
		var id, title, slug, img, brand, catName, catSlug string
		var pmin, pmax float64
		rows.Scan(&id, &title, &slug, &img, &pmin, &pmax, &brand, &catName, &catSlug)
		products = append(products, fiber.Map{"id": id, "title": title, "slug": slug, "image_url": img, "price_min": pmin, "price_max": pmax, "brand": brand, "category_name": catName, "category_slug": catSlug})
	}
	if products == nil {
		products = []fiber.Map{}
	}
	return c.JSON(fiber.Map{"success": true, "data": products})
}

func (h *Handlers) GetProductBySlug(c *fiber.Ctx) error {
	slug := c.Params("slug")
	ctx := context.Background()
	var id, title, pslug, desc, shortDesc, ean, sku, mpn, brand, img, stockStatus, catID, catName, catSlug, affiliateURL string
	var priceMin, priceMax float64
	var isActive bool
	var createdAt time.Time
	err := h.db.Pool.QueryRow(ctx, `
		SELECT p.id, p.title, p.slug, COALESCE(p.description,''), COALESCE(p.short_description,''),
		       COALESCE(p.ean,''), COALESCE(p.sku,''), COALESCE(p.mpn,''), COALESCE(p.brand,''),
		       COALESCE(p.image_url,''), COALESCE(p.stock_status,'instock'),
		       COALESCE(p.category_id::text,''), COALESCE(c.name,''), COALESCE(c.slug,''),
		       COALESCE(p.affiliate_url,''),
		       p.price_min, p.price_max, p.is_active, p.created_at
		FROM products p LEFT JOIN categories c ON p.category_id = c.id WHERE p.slug = $1
	`, slug).Scan(&id, &title, &pslug, &desc, &shortDesc, &ean, &sku, &mpn, &brand, &img, &stockStatus, &catID, &catName, &catSlug, &affiliateURL, &priceMin, &priceMax, &isActive, &createdAt)
	if err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "error": "Product not found"})
	}

	imgRows, _ := h.db.Pool.Query(ctx, `SELECT url FROM product_images WHERE product_id = $1::uuid ORDER BY position`, id)
	defer imgRows.Close()
	var images []string
	for imgRows.Next() {
		var imgURL string
		imgRows.Scan(&imgURL)
		images = append(images, imgURL)
	}

	attrRows, _ := h.db.Pool.Query(ctx, `SELECT attribute_name, attribute_value FROM product_attributes WHERE product_id = $1::uuid ORDER BY attribute_name`, id)
	defer attrRows.Close()
	var attributes []fiber.Map
	for attrRows.Next() {
		var name, value string
		attrRows.Scan(&name, &value)
		attributes = append(attributes, fiber.Map{"name": name, "value": value})
	}

	return c.JSON(fiber.Map{"success": true, "data": fiber.Map{
		"id": id, "title": title, "slug": pslug, "description": desc, "short_description": shortDesc,
		"ean": ean, "sku": sku, "mpn": mpn, "brand": brand, "image_url": img, "images": images,
		"stock_status": stockStatus, "category_id": catID, "category_name": catName, "category_slug": catSlug,
		"affiliate_url": affiliateURL, "price_min": priceMin, "price_max": priceMax, "is_active": isActive,
		"created_at": createdAt, "attributes": attributes,
	}})
}

func (h *Handlers) GetCategories(c *fiber.Ctx) error {
	ctx := context.Background()
	rows, _ := h.db.Pool.Query(ctx, `SELECT id, COALESCE(parent_id::text,''), name, slug, COALESCE(icon,''), product_count FROM categories WHERE is_active=true ORDER BY sort_order, name`)
	defer rows.Close()

	var cats []fiber.Map
	for rows.Next() {
		var id, parentID, name, slug, icon string
		var productCount int
		rows.Scan(&id, &parentID, &name, &slug, &icon, &productCount)
		cats = append(cats, fiber.Map{"id": id, "parent_id": parentID, "name": name, "slug": slug, "icon": icon, "product_count": productCount})
	}
	if cats == nil {
		cats = []fiber.Map{}
	}
	return c.JSON(fiber.Map{"success": true, "data": cats})
}

func (h *Handlers) GetCategoriesTree(c *fiber.Ctx) error {
	ctx := context.Background()
	rows, _ := h.db.Pool.Query(ctx, `SELECT id, COALESCE(parent_id::text,''), name, slug, COALESCE(icon,''), product_count FROM categories WHERE is_active=true ORDER BY sort_order, name`)
	defer rows.Close()

	type Cat struct {
		ID           string `json:"id"`
		ParentID     string `json:"parent_id,omitempty"`
		Name         string `json:"name"`
		Slug         string `json:"slug"`
		Icon         string `json:"icon,omitempty"`
		ProductCount int    `json:"product_count"`
		Children     []*Cat `json:"children,omitempty"`
	}
	var cats []*Cat
	catMap := make(map[string]*Cat)
	for rows.Next() {
		cat := &Cat{}
		rows.Scan(&cat.ID, &cat.ParentID, &cat.Name, &cat.Slug, &cat.Icon, &cat.ProductCount)
		cats = append(cats, cat)
		catMap[cat.ID] = cat
	}

	var roots []*Cat
	for _, cat := range cats {
		if cat.ParentID == "" {
			roots = append(roots, cat)
		} else if parent, ok := catMap[cat.ParentID]; ok {
			parent.Children = append(parent.Children, cat)
		}
	}
	if roots == nil {
		roots = []*Cat{}
	}
	return c.JSON(fiber.Map{"success": true, "data": roots})
}

func (h *Handlers) GetCategoriesFlat(c *fiber.Ctx) error {
	ctx := context.Background()
	rows, _ := h.db.Pool.Query(ctx, `SELECT id, COALESCE(parent_id::text,''), name, slug, COALESCE(icon,''), product_count FROM categories WHERE is_active=true ORDER BY name`)
	defer rows.Close()

	var cats []fiber.Map
	for rows.Next() {
		var id, parentID, name, slug, icon string
		var productCount int
		rows.Scan(&id, &parentID, &name, &slug, &icon, &productCount)
		cats = append(cats, fiber.Map{"id": id, "parent_id": parentID, "name": name, "slug": slug, "icon": icon, "product_count": productCount})
	}
	if cats == nil {
		cats = []fiber.Map{}
	}
	return c.JSON(fiber.Map{"success": true, "data": cats})
}

func (h *Handlers) GetCategoryBySlug(c *fiber.Ctx) error {
	slug := c.Params("slug")
	ctx := context.Background()
	var id, parentID, name, cslug, desc, icon string
	var productCount int
	err := h.db.Pool.QueryRow(ctx, `SELECT id, COALESCE(parent_id::text,''), name, slug, COALESCE(description,''), COALESCE(icon,''), product_count FROM categories WHERE slug = $1 AND is_active=true`, slug).Scan(&id, &parentID, &name, &cslug, &desc, &icon, &productCount)
	if err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "error": "Category not found"})
	}

	subRows, _ := h.db.Pool.Query(ctx, `SELECT id, name, slug, product_count FROM categories WHERE parent_id = $1::uuid AND is_active=true ORDER BY sort_order, name`, id)
	defer subRows.Close()
	var subcategories []fiber.Map
	for subRows.Next() {
		var subID, subName, subSlug string
		var subCount int
		subRows.Scan(&subID, &subName, &subSlug, &subCount)
		subcategories = append(subcategories, fiber.Map{"id": subID, "name": subName, "slug": subSlug, "product_count": subCount})
	}

	return c.JSON(fiber.Map{"success": true, "data": fiber.Map{
		"id": id, "parent_id": parentID, "name": name, "slug": cslug, "description": desc,
		"icon": icon, "product_count": productCount, "subcategories": subcategories,
	}})
}

func (h *Handlers) GetProductsByCategory(c *fiber.Ctx) error {
	slug := c.Params("slug")
	ctx := context.Background()
	var categoryID string
	err := h.db.Pool.QueryRow(ctx, "SELECT id FROM categories WHERE slug = $1", slug).Scan(&categoryID)
	if err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "error": "Category not found"})
	}

	rows, _ := h.db.Pool.Query(ctx, `SELECT p.id, p.title, p.slug, COALESCE(p.image_url,''), p.price_min, p.price_max, COALESCE(p.brand,'') FROM products p WHERE p.category_id = $1::uuid AND p.is_active=true ORDER BY p.created_at DESC`, categoryID)
	defer rows.Close()
	var products []fiber.Map
	for rows.Next() {
		var id, title, slug, img, brand string
		var pmin, pmax float64
		rows.Scan(&id, &title, &slug, &img, &pmin, &pmax, &brand)
		products = append(products, fiber.Map{"id": id, "title": title, "slug": slug, "image_url": img, "price_min": pmin, "price_max": pmax, "brand": brand})
	}
	if products == nil {
		products = []fiber.Map{}
	}
	return c.JSON(fiber.Map{"success": true, "data": products})
}

func (h *Handlers) GetStats(c *fiber.Ctx) error {
	ctx := context.Background()
	var p, cat int64
	h.db.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM products WHERE is_active=true").Scan(&p)
	h.db.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM categories WHERE is_active=true").Scan(&cat)
	return c.JSON(fiber.Map{"success": true, "data": fiber.Map{"products": p, "categories": cat}})
}

func (h *Handlers) AdminDashboard(c *fiber.Ctx) error { return h.GetStats(c) }

func (h *Handlers) GetProductOffers(c *fiber.Ctx) error {
	productID := c.Params("id")
	ctx := context.Background()

	var priceMin float64
	var stockStatus, affiliateURL string
	h.db.Pool.QueryRow(ctx, "SELECT price_min, COALESCE(stock_status,'instock'), COALESCE(affiliate_url,'') FROM products WHERE id = $1::uuid", productID).Scan(&priceMin, &stockStatus, &affiliateURL)

	shippingPrice := 2.99
	if priceMin >= 49 {
		shippingPrice = 0
	}

	return c.JSON(fiber.Map{"success": true, "data": []fiber.Map{{
		"id": "default", "vendor_id": "megabuy", "vendor_name": "MegaBuy.sk",
		"vendor_logo": "", "vendor_rating": 4.8, "vendor_reviews": 1250,
		"price": priceMin, "shipping_price": shippingPrice, "delivery_days": "1-2",
		"stock_status": stockStatus, "stock_quantity": 10, "is_megabuy": true, "affiliate_url": affiliateURL,
	}}})
}

// ========== ATTRIBUTE STATS ==========

func (h *Handlers) GetAttributeStats(c *fiber.Ctx) error {
	ctx := context.Background()

	rows, _ := h.db.Pool.Query(ctx, `
		SELECT attribute_name, attribute_slug, 
		       COUNT(DISTINCT product_id) as product_count,
		       COUNT(DISTINCT attribute_value) as value_count
		FROM product_attributes 
		GROUP BY attribute_name, attribute_slug 
		ORDER BY product_count DESC
	`)
	defer rows.Close()

	var attributes []fiber.Map
	for rows.Next() {
		var name, slug string
		var productCount, valueCount int
		rows.Scan(&name, &slug, &productCount, &valueCount)
		attributes = append(attributes, fiber.Map{
			"name":          name,
			"slug":          slug,
			"product_count": productCount,
			"value_count":   valueCount,
		})
	}
	if attributes == nil {
		attributes = []fiber.Map{}
	}
	return c.JSON(fiber.Map{"success": true, "data": attributes})
}

func (h *Handlers) GetFilterSettings(c *fiber.Ctx) error {
	ctx := context.Background()

	var settings string
	err := h.db.Pool.QueryRow(ctx, "SELECT settings FROM filter_settings WHERE id = 1").Scan(&settings)
	if err != nil {
		return c.JSON(fiber.Map{"success": true, "data": fiber.Map{
			"filterable_attributes": []string{},
			"show_price_filter":     true,
			"show_stock_filter":     true,
			"show_brand_filter":     true,
			"max_values_per_filter": 20,
		}})
	}
	return c.JSON(fiber.Map{"success": true, "data": settings})
}

func (h *Handlers) UpdateFilterSettings(c *fiber.Ctx) error {
	ctx := context.Background()
	body := c.Body()

	_, err := h.db.Pool.Exec(ctx, `
		INSERT INTO filter_settings (id, settings, updated_at) 
		VALUES (1, $1, NOW())
		ON CONFLICT (id) DO UPDATE SET settings = $1, updated_at = NOW()
	`, string(body))
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error()})
	}
	return c.JSON(fiber.Map{"success": true, "message": "Filter settings updated"})
}

// ========== ADMIN API ==========

func (h *Handlers) AdminProducts(c *fiber.Ctx) error {
	page := c.QueryInt("page", 1)
	limit := c.QueryInt("limit", 20)
	search := c.Query("search")
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * limit
	ctx := context.Background()

	var total int
	if search != "" {
		h.db.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM products WHERE title ILIKE $1 OR ean ILIKE $1", "%"+search+"%").Scan(&total)
	} else {
		h.db.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM products").Scan(&total)
	}

	var rows interface{ Close(); Next() bool; Scan(...interface{}) error }
	var err error
	if search != "" {
		rows, err = h.db.Pool.Query(ctx, `SELECT p.id, p.title, p.slug, COALESCE(p.ean,''), COALESCE(p.sku,''), COALESCE(p.image_url,''), p.price_min, p.price_max, p.is_active, COALESCE(p.stock_status,'instock'), COALESCE(c.name,''), p.created_at FROM products p LEFT JOIN categories c ON p.category_id = c.id WHERE p.title ILIKE $3 OR p.ean ILIKE $3 ORDER BY p.created_at DESC LIMIT $1 OFFSET $2`, limit, offset, "%"+search+"%")
	} else {
		rows, err = h.db.Pool.Query(ctx, `SELECT p.id, p.title, p.slug, COALESCE(p.ean,''), COALESCE(p.sku,''), COALESCE(p.image_url,''), p.price_min, p.price_max, p.is_active, COALESCE(p.stock_status,'instock'), COALESCE(c.name,''), p.created_at FROM products p LEFT JOIN categories c ON p.category_id = c.id ORDER BY p.created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	}
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error()})
	}
	defer rows.Close()

	var products []fiber.Map
	for rows.Next() {
		var id, title, slug, ean, sku, img, stockStatus, catName string
		var pmin, pmax float64
		var isActive bool
		var createdAt time.Time
		rows.Scan(&id, &title, &slug, &ean, &sku, &img, &pmin, &pmax, &isActive, &stockStatus, &catName, &createdAt)
		products = append(products, fiber.Map{"id": id, "title": title, "slug": slug, "ean": ean, "sku": sku, "image_url": img, "price_min": pmin, "price_max": pmax, "is_active": isActive, "stock_status": stockStatus, "category_name": catName, "created_at": createdAt})
	}
	if products == nil {
		products = []fiber.Map{}
	}
	return c.JSON(fiber.Map{"success": true, "data": fiber.Map{"items": products, "total": total, "page": page, "limit": limit, "total_pages": (total + limit - 1) / limit}})
}

func (h *Handlers) AdminGetProduct(c *fiber.Ctx) error {
	productID := c.Params("id")
	ctx := context.Background()
	var id, title, slug, desc, shortDesc, ean, sku, mpn, brand, img, stockStatus, catID string
	var priceMin, priceMax float64
	var isActive, isFeatured bool
	var createdAt, updatedAt time.Time
	err := h.db.Pool.QueryRow(ctx, `SELECT id, title, slug, COALESCE(description,''), COALESCE(short_description,''), COALESCE(ean,''), COALESCE(sku,''), COALESCE(mpn,''), COALESCE(brand,''), COALESCE(image_url,''), COALESCE(stock_status,'instock'), COALESCE(category_id::text,''), price_min, price_max, is_active, COALESCE(is_featured,false), created_at, updated_at FROM products WHERE id = $1::uuid`, productID).Scan(&id, &title, &slug, &desc, &shortDesc, &ean, &sku, &mpn, &brand, &img, &stockStatus, &catID, &priceMin, &priceMax, &isActive, &isFeatured, &createdAt, &updatedAt)
	if err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "error": "Product not found"})
	}

	imgRows, _ := h.db.Pool.Query(ctx, `SELECT id, url, COALESCE(alt,''), position, is_main FROM product_images WHERE product_id = $1::uuid ORDER BY position`, productID)
	defer imgRows.Close()
	var images []fiber.Map
	for imgRows.Next() {
		var imgID, imgURL, imgAlt string
		var imgPos int
		var imgMain bool
		imgRows.Scan(&imgID, &imgURL, &imgAlt, &imgPos, &imgMain)
		images = append(images, fiber.Map{"id": imgID, "url": imgURL, "alt": imgAlt, "position": imgPos, "is_main": imgMain})
	}

	return c.JSON(fiber.Map{"success": true, "data": fiber.Map{"id": id, "title": title, "slug": slug, "description": desc, "short_description": shortDesc, "ean": ean, "sku": sku, "mpn": mpn, "brand": brand, "image_url": img, "images": images, "stock_status": stockStatus, "category_id": catID, "price_min": priceMin, "price_max": priceMax, "is_active": isActive, "is_featured": isFeatured, "created_at": createdAt, "updated_at": updatedAt}})
}

func (h *Handlers) AdminCreateProduct(c *fiber.Ctx) error {
	var input struct {
		Title            string  `json:"title"`
		Slug             string  `json:"slug"`
		Description      string  `json:"description"`
		ShortDescription string  `json:"short_description"`
		EAN              string  `json:"ean"`
		SKU              string  `json:"sku"`
		MPN              string  `json:"mpn"`
		Brand            string  `json:"brand_name"`
		CategoryID       string  `json:"category_id"`
		ImageURL         string  `json:"image_url"`
		PriceMin         float64 `json:"price_min"`
		PriceMax         float64 `json:"price_max"`
		StockStatus      string  `json:"stock_status"`
		IsActive         bool    `json:"is_active"`
	}
	if err := c.BodyParser(&input); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "Invalid request"})
	}
	if input.Title == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "Title required"})
	}
	if input.Slug == "" {
		input.Slug = makeSlug(input.Title)
	}
	if input.StockStatus == "" {
		input.StockStatus = "instock"
	}
	if input.PriceMax == 0 && input.PriceMin > 0 {
		input.PriceMax = input.PriceMin
	}

	ctx := context.Background()
	productID := uuid.New()
	var catID interface{} = nil
	if input.CategoryID != "" {
		catID = input.CategoryID
	}

	_, err := h.db.Pool.Exec(ctx, `INSERT INTO products (id, category_id, title, slug, description, short_description, ean, sku, mpn, brand, image_url, price_min, price_max, stock_status, is_active, created_at, updated_at) VALUES ($1, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, NOW(), NOW())`, productID, catID, input.Title, input.Slug, input.Description, input.ShortDescription, input.EAN, input.SKU, input.MPN, input.Brand, input.ImageURL, input.PriceMin, input.PriceMax, input.StockStatus, input.IsActive)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error()})
	}

	if input.CategoryID != "" {
		h.db.Pool.Exec(ctx, `UPDATE categories SET product_count = (SELECT COUNT(*) FROM products WHERE category_id = $1::uuid AND is_active=true) WHERE id = $1::uuid`, input.CategoryID)
	}

	return c.Status(201).JSON(fiber.Map{"success": true, "data": fiber.Map{"id": productID.String(), "slug": input.Slug}})
}

func (h *Handlers) AdminUpdateProduct(c *fiber.Ctx) error {
	productID := c.Params("id")
	var input struct {
		Title            string  `json:"title"`
		Slug             string  `json:"slug"`
		Description      string  `json:"description"`
		ShortDescription string  `json:"short_description"`
		EAN              string  `json:"ean"`
		SKU              string  `json:"sku"`
		MPN              string  `json:"mpn"`
		Brand            string  `json:"brand_name"`
		CategoryID       string  `json:"category_id"`
		ImageURL         string  `json:"image_url"`
		PriceMin         float64 `json:"price_min"`
		PriceMax         float64 `json:"price_max"`
		StockStatus      string  `json:"stock_status"`
		IsActive         bool    `json:"is_active"`
	}
	if err := c.BodyParser(&input); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "Invalid request"})
	}

	ctx := context.Background()
	var catID interface{} = nil
	if input.CategoryID != "" {
		catID = input.CategoryID
	}

	_, err := h.db.Pool.Exec(ctx, `UPDATE products SET category_id = $2::uuid, title = COALESCE(NULLIF($3,''), title), slug = COALESCE(NULLIF($4,''), slug), description = $5, short_description = $6, ean = $7, sku = $8, mpn = $9, brand = $10, image_url = $11, price_min = $12, price_max = $13, stock_status = $14, is_active = $15, updated_at = NOW() WHERE id = $1::uuid`, productID, catID, input.Title, input.Slug, input.Description, input.ShortDescription, input.EAN, input.SKU, input.MPN, input.Brand, input.ImageURL, input.PriceMin, input.PriceMax, input.StockStatus, input.IsActive)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error()})
	}

	return c.JSON(fiber.Map{"success": true, "message": "Product updated"})
}

func (h *Handlers) AdminDeleteProduct(c *fiber.Ctx) error {
	productID := c.Params("id")
	ctx := context.Background()
	h.db.Pool.Exec(ctx, "DELETE FROM product_images WHERE product_id = $1::uuid", productID)
	h.db.Pool.Exec(ctx, "DELETE FROM product_attributes WHERE product_id = $1::uuid", productID)
	_, err := h.db.Pool.Exec(ctx, "DELETE FROM products WHERE id = $1::uuid", productID)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error()})
	}
	if h.es != nil {
		h.es.DeleteProduct(productID)
	}
	return c.JSON(fiber.Map{"success": true, "message": "Product deleted"})
}

func (h *Handlers) DeleteAllProducts(c *fiber.Ctx) error {
	ctx := context.Background()

	var count int
	h.db.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM products").Scan(&count)

	h.db.Pool.Exec(ctx, "DELETE FROM product_images")
	h.db.Pool.Exec(ctx, "DELETE FROM product_attributes")
	h.db.Pool.Exec(ctx, "DELETE FROM products")
	h.db.Pool.Exec(ctx, "UPDATE categories SET product_count = 0")

	os.RemoveAll("./uploads/products")
	os.MkdirAll("./uploads/products", 0755)

	if h.es != nil {
		h.es.DeleteIndex()
		h.es.CreateIndex()
	}

	return c.JSON(fiber.Map{"success": true, "message": fmt.Sprintf("Deleted %d products", count), "count": count})
}

func (h *Handlers) BulkDeleteProducts(c *fiber.Ctx) error {
	var input struct {
		IDs    []string `json:"ids"`
		Action string   `json:"action"`
	}
	if err := c.BodyParser(&input); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "Invalid request"})
	}

	ctx := context.Background()

	switch input.Action {
	case "delete":
		for _, id := range input.IDs {
			h.db.Pool.Exec(ctx, "DELETE FROM product_images WHERE product_id = $1::uuid", id)
			h.db.Pool.Exec(ctx, "DELETE FROM product_attributes WHERE product_id = $1::uuid", id)
			h.db.Pool.Exec(ctx, "DELETE FROM products WHERE id = $1::uuid", id)
			if h.es != nil {
				h.es.DeleteProduct(id)
			}
		}
	case "activate":
		for _, id := range input.IDs {
			h.db.Pool.Exec(ctx, "UPDATE products SET is_active = true WHERE id = $1::uuid", id)
		}
	case "deactivate":
		for _, id := range input.IDs {
			h.db.Pool.Exec(ctx, "UPDATE products SET is_active = false WHERE id = $1::uuid", id)
		}
	}

	return c.JSON(fiber.Map{"success": true, "message": fmt.Sprintf("Processed %d products", len(input.IDs))})
}

func (h *Handlers) AdminCategories(c *fiber.Ctx) error {
	ctx := context.Background()
	rows, _ := h.db.Pool.Query(ctx, `SELECT id, COALESCE(parent_id::text,''), name, slug, COALESCE(icon,''), product_count, is_active FROM categories ORDER BY sort_order, name`)
	defer rows.Close()

	var cats []fiber.Map
	for rows.Next() {
		var id, parentID, name, slug, icon string
		var productCount int
		var isActive bool
		rows.Scan(&id, &parentID, &name, &slug, &icon, &productCount, &isActive)
		cats = append(cats, fiber.Map{"id": id, "parent_id": parentID, "name": name, "slug": slug, "icon": icon, "product_count": productCount, "is_active": isActive})
	}
	if cats == nil {
		cats = []fiber.Map{}
	}
	return c.JSON(fiber.Map{"success": true, "data": cats})
}

func (h *Handlers) AdminCreateCategory(c *fiber.Ctx) error {
	var input struct {
		ParentID    string `json:"parent_id"`
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
		Icon        string `json:"icon"`
	}
	if err := c.BodyParser(&input); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "Invalid request"})
	}
	if input.Name == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "Name required"})
	}
	if input.Slug == "" {
		input.Slug = makeSlug(input.Name)
	}

	ctx := context.Background()
	id := uuid.New()
	var err error
	if input.ParentID != "" {
		_, err = h.db.Pool.Exec(ctx, `INSERT INTO categories (id, parent_id, name, slug, description, icon, is_active, created_at, updated_at) VALUES ($1, $2::uuid, $3, $4, $5, $6, true, NOW(), NOW())`, id, input.ParentID, input.Name, input.Slug, input.Description, input.Icon)
	} else {
		_, err = h.db.Pool.Exec(ctx, `INSERT INTO categories (id, name, slug, description, icon, is_active, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, true, NOW(), NOW())`, id, input.Name, input.Slug, input.Description, input.Icon)
	}
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error()})
	}
	return c.Status(201).JSON(fiber.Map{"success": true, "data": fiber.Map{"id": id.String(), "slug": input.Slug}})
}

func (h *Handlers) AdminUpdateCategory(c *fiber.Ctx) error {
	categoryID := c.Params("id")
	var input struct {
		ParentID    string `json:"parent_id"`
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
		Icon        string `json:"icon"`
		IsActive    bool   `json:"is_active"`
	}
	if err := c.BodyParser(&input); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "Invalid request"})
	}

	ctx := context.Background()
	var err error
	if input.ParentID != "" {
		_, err = h.db.Pool.Exec(ctx, `UPDATE categories SET parent_id = $2::uuid, name = COALESCE(NULLIF($3,''), name), slug = COALESCE(NULLIF($4,''), slug), description = $5, icon = $6, is_active = $7, updated_at = NOW() WHERE id = $1::uuid`, categoryID, input.ParentID, input.Name, input.Slug, input.Description, input.Icon, input.IsActive)
	} else {
		_, err = h.db.Pool.Exec(ctx, `UPDATE categories SET parent_id = NULL, name = COALESCE(NULLIF($2,''), name), slug = COALESCE(NULLIF($3,''), slug), description = $4, icon = $5, is_active = $6, updated_at = NOW() WHERE id = $1::uuid`, categoryID, input.Name, input.Slug, input.Description, input.Icon, input.IsActive)
	}
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error()})
	}
	return c.JSON(fiber.Map{"success": true, "message": "Category updated"})
}

func (h *Handlers) AdminDeleteCategory(c *fiber.Ctx) error {
	categoryID := c.Params("id")
	ctx := context.Background()
	h.db.Pool.Exec(ctx, "UPDATE categories SET parent_id = NULL WHERE parent_id = $1::uuid", categoryID)
	_, err := h.db.Pool.Exec(ctx, "DELETE FROM categories WHERE id = $1::uuid", categoryID)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error()})
	}
	return c.JSON(fiber.Map{"success": true, "message": "Category deleted"})
}

func (h *Handlers) UploadImage(c *fiber.Ctx) error {
	file, err := c.FormFile("file")
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "No file uploaded"})
	}
	uploadDir := "./uploads"
	os.MkdirAll(uploadDir, 0755)
	ext := filepath.Ext(file.Filename)
	filename := fmt.Sprintf("%s%s", uuid.New().String(), ext)
	fpath := fmt.Sprintf("%s/%s", uploadDir, filename)
	if err := c.SaveFile(file, fpath); err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "error": "Failed to save file"})
	}
	baseURL := c.BaseURL()
	url := fmt.Sprintf("%s/uploads/%s", baseURL, filename)
	return c.JSON(fiber.Map{"success": true, "data": fiber.Map{"url": url, "filename": filename}})
}

// ========== FEED HANDLERS ==========

type HeurekaFeed struct {
	XMLName   xml.Name         `xml:"SHOP"`
	ShopItems []HeurekaProduct `xml:"SHOPITEM"`
}

type HeurekaProduct struct {
	ItemID          string         `xml:"ITEM_ID"`
	ProductName     string         `xml:"PRODUCTNAME"`
	Product         string         `xml:"PRODUCT"`
	Description     string         `xml:"DESCRIPTION"`
	URL             string         `xml:"URL"`
	ImageURL        string         `xml:"IMGURL"`
	ImageURLAlt     []string       `xml:"IMGURL_ALTERNATIVE"`
	Price           float64        `xml:"PRICE_VAT"`
	PriceVAT        float64        `xml:"PRICE_VAT"`
	Manufacturer    string         `xml:"MANUFACTURER"`
	CategoryText    string         `xml:"CATEGORYTEXT"`
	EAN             string         `xml:"EAN"`
	ProductNo       string         `xml:"PRODUCTNO"`
	DeliveryDate    string         `xml:"DELIVERY_DATE"`
	Params          []HeurekaParam `xml:"PARAM"`
	ExtraMessage    string         `xml:"EXTRA_MESSAGE"`
	ItemGroupID     string         `xml:"ITEMGROUP_ID"`
	Accessory       []string       `xml:"ACCESSORY"`
	Gift            string         `xml:"GIFT"`
	ExtendedWarrant string         `xml:"EXTENDED_WARRANTY"`
}

type HeurekaParam struct {
	ParamName string `xml:"PARAM_NAME"`
	Val       string `xml:"VAL"`
}

func (h *Handlers) GetFeeds(c *fiber.Ctx) error {
	ctx := context.Background()
	rows, err := h.db.Pool.Query(ctx, `SELECT id, name, url, feed_type, COALESCE(category_mapping,'{}'), is_active, last_import, import_count, created_at FROM feeds ORDER BY created_at DESC`)
	if err != nil {
		log.Printf("GetFeeds error: %v", err)
		return c.JSON(fiber.Map{"success": true, "data": []fiber.Map{}})
	}
	defer rows.Close()

	var feeds []fiber.Map
	for rows.Next() {
		var id, name, url, feedType, categoryMapping string
		var isActive bool
		var lastImport *time.Time
		var importCount int
		var createdAt time.Time
		rows.Scan(&id, &name, &url, &feedType, &categoryMapping, &isActive, &lastImport, &importCount, &createdAt)
		feeds = append(feeds, fiber.Map{
			"id": id, "name": name, "url": url, "feed_type": feedType,
			"category_mapping": categoryMapping, "is_active": isActive,
			"last_import": lastImport, "import_count": importCount, "created_at": createdAt,
		})
	}
	if feeds == nil {
		feeds = []fiber.Map{}
	}
	return c.JSON(fiber.Map{"success": true, "data": feeds})
}

func (h *Handlers) CreateFeed(c *fiber.Ctx) error {
	var input struct {
		Name            string `json:"name"`
		URL             string `json:"url"`
		FeedType        string `json:"feed_type"`
		CategoryMapping string `json:"category_mapping"`
		IsActive        bool   `json:"is_active"`
	}
	if err := c.BodyParser(&input); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "Invalid request"})
	}
	if input.Name == "" || input.URL == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "Name and URL required"})
	}
	if input.FeedType == "" {
		input.FeedType = "heureka"
	}

	ctx := context.Background()
	id := uuid.New()
	_, err := h.db.Pool.Exec(ctx, `INSERT INTO feeds (id, name, url, feed_type, category_mapping, is_active, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW())`, id, input.Name, input.URL, input.FeedType, input.CategoryMapping, input.IsActive)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error()})
	}
	return c.Status(201).JSON(fiber.Map{"success": true, "data": fiber.Map{"id": id.String()}})
}

func (h *Handlers) PreviewFeed(c *fiber.Ctx) error {
	var input struct {
		URL string `json:"url"`
	}
	if err := c.BodyParser(&input); err != nil {
		log.Printf("PreviewFeed: Invalid request body: %v", err)
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "Invalid request body"})
	}

	if input.URL == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "URL is required"})
	}

	log.Printf("PreviewFeed: Fetching URL: %s", input.URL)

	// Create HTTP client with timeout
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(input.URL)
	if err != nil {
		log.Printf("PreviewFeed: Failed to fetch: %v", err)
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "Failed to fetch feed: " + err.Error()})
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": fmt.Sprintf("Feed returned status %d", resp.StatusCode)})
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("PreviewFeed: Failed to read body: %v", err)
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "Failed to read feed: " + err.Error()})
	}

	log.Printf("PreviewFeed: Downloaded %d bytes", len(body))

	var feed HeurekaFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		log.Printf("PreviewFeed: Failed to parse XML: %v", err)
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "Failed to parse feed: " + err.Error()})
	}

	log.Printf("PreviewFeed: Parsed %d items", len(feed.ShopItems))

	// Get sample products with safe string handling
	limit := 5
	if len(feed.ShopItems) < limit {
		limit = len(feed.ShopItems)
	}

	var samples []fiber.Map
	for i := 0; i < limit; i++ {
		item := feed.ShopItems[i]
		var params []fiber.Map
		for _, p := range item.Params {
			if p.ParamName != "" {
				params = append(params, fiber.Map{"name": p.ParamName, "value": p.Val})
			}
		}

		// Safe truncation
		desc := item.Description
		if len(desc) > 200 {
			desc = desc[:200] + "..."
		}

		samples = append(samples, fiber.Map{
			"title":       item.ProductName,
			"description": desc,
			"price":       item.PriceVAT,
			"image":       item.ImageURL,
			"category":    item.CategoryText,
			"brand":       item.Manufacturer,
			"ean":         item.EAN,
			"params":      params,
		})
	}

	// Get unique categories
	catMap := make(map[string]int)
	for _, item := range feed.ShopItems {
		if item.CategoryText != "" {
			catMap[item.CategoryText]++
		}
	}
	var categories []fiber.Map
	for cat, count := range catMap {
		categories = append(categories, fiber.Map{"name": cat, "count": count})
	}

	// Get unique attributes (PARAMs)
	attrMap := make(map[string]int)
	for _, item := range feed.ShopItems {
		for _, p := range item.Params {
			if p.ParamName != "" {
				attrMap[p.ParamName]++
			}
		}
	}
	var attributes []fiber.Map
	for attr, count := range attrMap {
		attributes = append(attributes, fiber.Map{"name": attr, "count": count})
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data": fiber.Map{
			"total_products": len(feed.ShopItems),
			"samples":        samples,
			"categories":     categories,
			"attributes":     attributes,
		},
	})
}

func (h *Handlers) UpdateFeed(c *fiber.Ctx) error {
	feedID := c.Params("id")
	var input struct {
		Name            string `json:"name"`
		URL             string `json:"url"`
		FeedType        string `json:"feed_type"`
		CategoryMapping string `json:"category_mapping"`
		IsActive        bool   `json:"is_active"`
	}
	if err := c.BodyParser(&input); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "Invalid request"})
	}

	ctx := context.Background()
	_, err := h.db.Pool.Exec(ctx, `UPDATE feeds SET name = $2, url = $3, feed_type = $4, category_mapping = $5, is_active = $6, updated_at = NOW() WHERE id = $1::uuid`, feedID, input.Name, input.URL, input.FeedType, input.CategoryMapping, input.IsActive)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error()})
	}
	return c.JSON(fiber.Map{"success": true, "message": "Feed updated"})
}

func (h *Handlers) DeleteFeed(c *fiber.Ctx) error {
	feedID := c.Params("id")
	ctx := context.Background()
	_, err := h.db.Pool.Exec(ctx, "DELETE FROM feeds WHERE id = $1::uuid", feedID)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error()})
	}
	return c.JSON(fiber.Map{"success": true, "message": "Feed deleted"})
}

func (h *Handlers) StartImport(c *fiber.Ctx) error {
	feedID := c.Params("id")
	ctx := context.Background()

	var feedURL, feedType string
	err := h.db.Pool.QueryRow(ctx, "SELECT url, feed_type FROM feeds WHERE id = $1::uuid", feedID).Scan(&feedURL, &feedType)
	if err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "error": "Feed not found"})
	}

	go h.importFeed(feedID, feedURL, feedType)

	return c.JSON(fiber.Map{"success": true, "message": "Import started"})
}

func (h *Handlers) importFeed(feedID, feedURL, feedType string) {
	ctx := context.Background()

	progress := &ImportProgress{
		FeedID:    feedID,
		Status:    "downloading",
		StartedAt: time.Now().Format(time.RFC3339),
	}
	h.importProgress.Store(feedID, progress)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(feedURL)
	if err != nil {
		progress.Status = "failed"
		progress.Message = "Failed to download: " + err.Error()
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	progress.Status = "parsing"

	var feed HeurekaFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		progress.Status = "failed"
		progress.Message = "Failed to parse: " + err.Error()
		return
	}

	progress.Total = len(feed.ShopItems)
	progress.Status = "importing"

	for i, item := range feed.ShopItems {
		categoryID := h.findOrCreateCategory(ctx, item.CategoryText)

		var existingID string
		h.db.Pool.QueryRow(ctx, "SELECT id FROM products WHERE ean = $1 OR sku = $2", item.EAN, item.ItemID).Scan(&existingID)

		productID := existingID
		if productID == "" {
			productID = uuid.New().String()
		}

		slug := makeSlug(item.ProductName)

		stockStatus := "instock"
		if item.DeliveryDate != "" && item.DeliveryDate != "0" {
			stockStatus = "outofstock"
		}

		price := item.PriceVAT
		if price == 0 {
			price = item.Price
		}

		if existingID == "" {
			_, err = h.db.Pool.Exec(ctx, `
				INSERT INTO products (id, category_id, title, slug, description, ean, sku, brand, image_url, price_min, price_max, stock_status, affiliate_url, is_active, created_at, updated_at)
				VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $10, $11, $12, true, NOW(), NOW())
			`, productID, categoryID, item.ProductName, slug, item.Description, item.EAN, item.ItemID, item.Manufacturer, item.ImageURL, price, stockStatus, item.URL)
		} else {
			_, err = h.db.Pool.Exec(ctx, `
				UPDATE products SET category_id = $2::uuid, title = $3, description = $4, brand = $5, image_url = $6, price_min = $7, price_max = $7, stock_status = $8, affiliate_url = $9, updated_at = NOW()
				WHERE id = $1::uuid
			`, productID, categoryID, item.ProductName, item.Description, item.Manufacturer, item.ImageURL, price, stockStatus, item.URL)
		}

		if err == nil {
			progress.Success++

			for j, imgURL := range item.ImageURLAlt {
				h.db.Pool.Exec(ctx, `
					INSERT INTO product_images (id, product_id, url, position, is_main) 
					VALUES ($1::uuid, $2::uuid, $3, $4, false)
					ON CONFLICT DO NOTHING
				`, uuid.New().String(), productID, imgURL, j+1)
			}

			// Save attributes (PARAM tags)
			h.db.Pool.Exec(ctx, "DELETE FROM product_attributes WHERE product_id = $1::uuid", productID)
			for _, param := range item.Params {
				if param.ParamName != "" && param.Val != "" {
					attrSlug := makeSlug(param.ParamName)
					h.db.Pool.Exec(ctx, `
						INSERT INTO product_attributes (id, product_id, attribute_name, attribute_slug, attribute_value)
						VALUES ($1::uuid, $2::uuid, $3, $4, $5)
					`, uuid.New().String(), productID, param.ParamName, attrSlug, param.Val)
				}
			}
		} else {
			progress.Failed++
		}

		progress.Processed = i + 1
	}

	h.db.Pool.Exec(ctx, "UPDATE feeds SET last_import = NOW(), import_count = import_count + $2 WHERE id = $1::uuid", feedID, progress.Success)

	h.db.Pool.Exec(ctx, `
		UPDATE categories SET product_count = (
			SELECT COUNT(*) FROM products WHERE category_id = categories.id AND is_active = true
		)
	`)

	progress.Status = "completed"
	progress.Message = fmt.Sprintf("Imported %d products, %d failed", progress.Success, progress.Failed)
}

func (h *Handlers) findOrCreateCategory(ctx context.Context, categoryText string) string {
	if categoryText == "" {
		return ""
	}

	parts := strings.Split(categoryText, " | ")
	if len(parts) == 0 {
		parts = []string{categoryText}
	}

	var parentID *string
	var lastCategoryID string

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		slug := makeSlug(part)

		var catID string
		var err error

		if parentID == nil {
			err = h.db.Pool.QueryRow(ctx, "SELECT id FROM categories WHERE slug = $1 AND parent_id IS NULL", slug).Scan(&catID)
		} else {
			err = h.db.Pool.QueryRow(ctx, "SELECT id FROM categories WHERE slug = $1 AND parent_id = $2::uuid", slug, *parentID).Scan(&catID)
		}

		if err != nil {
			catID = uuid.New().String()
			if parentID == nil {
				h.db.Pool.Exec(ctx, `INSERT INTO categories (id, name, slug, is_active, created_at, updated_at) VALUES ($1::uuid, $2, $3, true, NOW(), NOW())`, catID, part, slug)
			} else {
				h.db.Pool.Exec(ctx, `INSERT INTO categories (id, parent_id, name, slug, is_active, created_at, updated_at) VALUES ($1::uuid, $2::uuid, $3, $4, true, NOW(), NOW())`, catID, *parentID, part, slug)
			}
		}

		parentID = &catID
		lastCategoryID = catID
	}

	return lastCategoryID
}

func (h *Handlers) GetImportProgress(c *fiber.Ctx) error {
	feedID := c.Params("id")
	if progress, ok := h.importProgress.Load(feedID); ok {
		return c.JSON(fiber.Map{"success": true, "data": progress})
	}
	return c.JSON(fiber.Map{"success": true, "data": fiber.Map{"status": "idle"}})
}
