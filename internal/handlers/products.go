package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"
)

type ProductHandler struct {
	db *pgxpool.Pool
	es *elasticsearch.Client
}

func NewProductHandler(db *pgxpool.Pool, es *elasticsearch.Client) *ProductHandler {
	return &ProductHandler{db: db, es: es}
}

type Product struct {
	ID               string            `json:"id"`
	Title            string            `json:"title"`
	Slug             string            `json:"slug"`
	Description      string            `json:"description,omitempty"`
	ShortDescription string            `json:"short_description,omitempty"`
	EAN              string            `json:"ean,omitempty"`
	SKU              string            `json:"sku,omitempty"`
	MPN              string            `json:"mpn,omitempty"`
	Brand            string            `json:"brand,omitempty"`
	ImageURL         string            `json:"image_url,omitempty"`
	GalleryImages    []string          `json:"gallery_images,omitempty"`
	CategoryID       string            `json:"category_id,omitempty"`
	CategoryName     string            `json:"category_name,omitempty"`
	CategorySlug     string            `json:"category_slug,omitempty"`
	PriceMin         float64           `json:"price_min"`
	PriceMax         float64           `json:"price_max"`
	StockStatus      string            `json:"stock_status"`
	StockQuantity    int               `json:"stock_quantity,omitempty"`
	IsActive         bool              `json:"is_active"`
	Attributes       map[string]string `json:"attributes,omitempty"`
	Rating           float64           `json:"rating,omitempty"`
	ReviewCount      int               `json:"review_count,omitempty"`
	FeedID           string            `json:"feed_id,omitempty"`
	AffiliateURL     string            `json:"affiliate_url,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
}

type Offer struct {
	ID            string  `json:"id"`
	ProductID     string  `json:"product_id"`
	VendorID      string  `json:"vendor_id"`
	VendorName    string  `json:"vendor_name"`
	VendorLogo    string  `json:"vendor_logo,omitempty"`
	VendorRating  float64 `json:"vendor_rating"`
	VendorReviews int     `json:"vendor_reviews"`
	Price         float64 `json:"price"`
	ShippingPrice float64 `json:"shipping_price"`
	DeliveryDays  string  `json:"delivery_days"`
	StockStatus   string  `json:"stock_status"`
	StockQuantity int     `json:"stock_quantity"`
	IsMegabuy     bool    `json:"is_megabuy"`
	AffiliateURL  string  `json:"affiliate_url,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// ==================== PUBLIC ENDPOINTS ====================

// GetProducts - public product listing with ES search
func (h *ProductHandler) GetProducts(c *fiber.Ctx) error {
	page := c.QueryInt("page", 1)
	limit := c.QueryInt("limit", 20)
	search := c.Query("search")
	category := c.Query("category")
	brand := c.Query("brand")
	minPrice := c.QueryFloat("min_price", 0)
	maxPrice := c.QueryFloat("max_price", 0)
	sort := c.Query("sort", "relevance")
	
	if page < 1 { page = 1 }
	if limit < 1 || limit > 100 { limit = 20 }
	offset := (page - 1) * limit

	// Try Elasticsearch first for search queries
	if search != "" && h.es != nil {
		return h.searchProductsES(c, search, category, brand, minPrice, maxPrice, sort, page, limit)
	}

	// Fallback to PostgreSQL
	return h.getProductsDB(c, category, brand, minPrice, maxPrice, sort, offset, limit)
}

func (h *ProductHandler) searchProductsES(c *fiber.Ctx, search, category, brand string, minPrice, maxPrice float64, sort string, page, limit int) error {
	must := []map[string]interface{}{
		{"multi_match": map[string]interface{}{
			"query":  search,
			"fields": []string{"title^3", "description", "brand^2", "ean", "sku"},
			"type":   "best_fields",
			"fuzziness": "AUTO",
		}},
		{"term": map[string]interface{}{"is_active": true}},
	}

	if category != "" {
		must = append(must, map[string]interface{}{"term": map[string]interface{}{"category_slug": category}})
	}
	if brand != "" {
		must = append(must, map[string]interface{}{"term": map[string]interface{}{"brand.keyword": brand}})
	}
	if minPrice > 0 || maxPrice > 0 {
		priceRange := map[string]interface{}{}
		if minPrice > 0 { priceRange["gte"] = minPrice }
		if maxPrice > 0 { priceRange["lte"] = maxPrice }
		must = append(must, map[string]interface{}{"range": map[string]interface{}{"price_min": priceRange}})
	}

	// Sorting
	sortField := []map[string]interface{}{{"_score": "desc"}}
	switch sort {
	case "price_asc":
		sortField = []map[string]interface{}{{"price_min": "asc"}}
	case "price_desc":
		sortField = []map[string]interface{}{{"price_min": "desc"}}
	case "newest":
		sortField = []map[string]interface{}{{"created_at": "desc"}}
	case "rating":
		sortField = []map[string]interface{}{{"rating": "desc"}}
	}

	query := map[string]interface{}{
		"query": map[string]interface{}{
			"bool": map[string]interface{}{"must": must},
		},
		"sort": sortField,
		"from": (page - 1) * limit,
		"size": limit,
		"aggs": map[string]interface{}{
			"brands": map[string]interface{}{
				"terms": map[string]interface{}{"field": "brand.keyword", "size": 50},
			},
			"categories": map[string]interface{}{
				"terms": map[string]interface{}{"field": "category_name.keyword", "size": 50},
			},
			"price_stats": map[string]interface{}{
				"stats": map[string]interface{}{"field": "price_min"},
			},
		},
	}

	queryJSON, _ := json.Marshal(query)
	res, err := h.es.Search(
		h.es.Search.WithContext(context.Background()),
		h.es.Search.WithIndex("products"),
		h.es.Search.WithBody(strings.NewReader(string(queryJSON))),
	)
	if err != nil {
		return h.getProductsDB(c, category, brand, minPrice, maxPrice, sort, (page-1)*limit, limit)
	}
	defer res.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(res.Body).Decode(&result)

	hits := result["hits"].(map[string]interface{})
	total := int(hits["total"].(map[string]interface{})["value"].(float64))
	
	products := []Product{}
	for _, hit := range hits["hits"].([]interface{}) {
		source := hit.(map[string]interface{})["_source"].(map[string]interface{})
		p := Product{
			ID:           getString(source, "id"),
			Title:        getString(source, "title"),
			Slug:         getString(source, "slug"),
			ImageURL:     getString(source, "image_url"),
			CategoryName: getString(source, "category_name"),
			CategorySlug: getString(source, "category_slug"),
			Brand:        getString(source, "brand"),
			PriceMin:     getFloat(source, "price_min"),
			PriceMax:     getFloat(source, "price_max"),
			StockStatus:  getString(source, "stock_status"),
			IsActive:     getBool(source, "is_active"),
			Rating:       getFloat(source, "rating"),
			ReviewCount:  getInt(source, "review_count"),
		}
		products = append(products, p)
	}

	// Extract facets
	aggs := result["aggregations"].(map[string]interface{})
	facets := map[string]interface{}{
		"brands":     extractBuckets(aggs, "brands"),
		"categories": extractBuckets(aggs, "categories"),
		"price_stats": aggs["price_stats"],
	}

	return c.JSON(fiber.Map{
		"items":  products,
		"total":  total,
		"page":   page,
		"limit":  limit,
		"facets": facets,
	})
}

func (h *ProductHandler) getProductsDB(c *fiber.Ctx, category, brand string, minPrice, maxPrice float64, sort string, offset, limit int) error {
	query := `
		SELECT p.id, p.title, p.slug, p.image_url, p.price_min, p.price_max, 
			   p.stock_status, p.is_active, p.brand, p.rating, p.review_count,
			   c.name as category_name, c.slug as category_slug
		FROM products p
		LEFT JOIN categories c ON p.category_id = c.id
		WHERE p.is_active = true
	`
	args := []interface{}{}
	argNum := 1

	if category != "" {
		query += fmt.Sprintf(" AND c.slug = $%d", argNum)
		args = append(args, category)
		argNum++
	}
	if brand != "" {
		query += fmt.Sprintf(" AND p.brand ILIKE $%d", argNum)
		args = append(args, brand)
		argNum++
	}
	if minPrice > 0 {
		query += fmt.Sprintf(" AND p.price_min >= $%d", argNum)
		args = append(args, minPrice)
		argNum++
	}
	if maxPrice > 0 {
		query += fmt.Sprintf(" AND p.price_min <= $%d", argNum)
		args = append(args, maxPrice)
		argNum++
	}

	// Sorting
	switch sort {
	case "price_asc":
		query += " ORDER BY p.price_min ASC"
	case "price_desc":
		query += " ORDER BY p.price_min DESC"
	case "newest":
		query += " ORDER BY p.created_at DESC"
	case "rating":
		query += " ORDER BY p.rating DESC NULLS LAST"
	default:
		query += " ORDER BY p.created_at DESC"
	}

	query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", argNum, argNum+1)
	args = append(args, limit, offset)

	rows, err := h.db.Query(context.Background(), query, args...)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	products := []Product{}
	for rows.Next() {
		var p Product
		var catName, catSlug *string
		err := rows.Scan(&p.ID, &p.Title, &p.Slug, &p.ImageURL, &p.PriceMin, &p.PriceMax,
			&p.StockStatus, &p.IsActive, &p.Brand, &p.Rating, &p.ReviewCount, &catName, &catSlug)
		if err != nil {
			continue
		}
		if catName != nil { p.CategoryName = *catName }
		if catSlug != nil { p.CategorySlug = *catSlug }
		products = append(products, p)
	}

	// Count total
	countQuery := `SELECT COUNT(*) FROM products p LEFT JOIN categories c ON p.category_id = c.id WHERE p.is_active = true`
	var total int
	h.db.QueryRow(context.Background(), countQuery).Scan(&total)

	return c.JSON(fiber.Map{
		"items": products,
		"total": total,
		"page":  offset/limit + 1,
		"limit": limit,
	})
}

// GetProductBySlug - get single product by slug
func (h *ProductHandler) GetProductBySlug(c *fiber.Ctx) error {
	slug := c.Params("slug")
	
	var p Product
	var catID, catName, catSlug *string
	var galleryJSON, attrsJSON []byte
	
	err := h.db.QueryRow(context.Background(), `
		SELECT p.id, p.title, p.slug, p.description, p.short_description,
			   p.ean, p.sku, p.mpn, p.brand, p.image_url, p.gallery_images,
			   p.category_id, c.name, c.slug, p.price_min, p.price_max,
			   p.stock_status, p.stock_quantity, p.is_active, p.attributes,
			   p.rating, p.review_count, p.affiliate_url, p.created_at, p.updated_at
		FROM products p
		LEFT JOIN categories c ON p.category_id = c.id
		WHERE p.slug = $1 AND p.is_active = true
	`, slug).Scan(
		&p.ID, &p.Title, &p.Slug, &p.Description, &p.ShortDescription,
		&p.EAN, &p.SKU, &p.MPN, &p.Brand, &p.ImageURL, &galleryJSON,
		&catID, &catName, &catSlug, &p.PriceMin, &p.PriceMax,
		&p.StockStatus, &p.StockQuantity, &p.IsActive, &attrsJSON,
		&p.Rating, &p.ReviewCount, &p.AffiliateURL, &p.CreatedAt, &p.UpdatedAt,
	)
	
	if err == pgx.ErrNoRows {
		return c.Status(404).JSON(fiber.Map{"error": "Product not found"})
	}
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	if catID != nil { p.CategoryID = *catID }
	if catName != nil { p.CategoryName = *catName }
	if catSlug != nil { p.CategorySlug = *catSlug }
	if galleryJSON != nil { json.Unmarshal(galleryJSON, &p.GalleryImages) }
	if attrsJSON != nil { json.Unmarshal(attrsJSON, &p.Attributes) }

	return c.JSON(p)
}

// GetProductOffers - get all offers for a product
func (h *ProductHandler) GetProductOffers(c *fiber.Ctx) error {
	productID := c.Params("id")
	
	rows, err := h.db.Query(context.Background(), `
		SELECT o.id, o.product_id, o.vendor_id, v.name, v.logo_url, v.rating, v.review_count,
			   o.price, o.shipping_price, o.delivery_days, o.stock_status, o.stock_quantity,
			   o.is_megabuy, o.affiliate_url, o.created_at
		FROM product_offers o
		LEFT JOIN vendors v ON o.vendor_id = v.id
		WHERE o.product_id = $1
		ORDER BY o.price ASC
	`, productID)
	
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	offers := []Offer{}
	for rows.Next() {
		var o Offer
		var vendorName, vendorLogo *string
		var vendorRating *float64
		var vendorReviews *int
		
		err := rows.Scan(
			&o.ID, &o.ProductID, &o.VendorID, &vendorName, &vendorLogo, &vendorRating, &vendorReviews,
			&o.Price, &o.ShippingPrice, &o.DeliveryDays, &o.StockStatus, &o.StockQuantity,
			&o.IsMegabuy, &o.AffiliateURL, &o.CreatedAt,
		)
		if err != nil {
			continue
		}
		
		if vendorName != nil { o.VendorName = *vendorName } else { o.VendorName = "MegaBuy.sk" }
		if vendorLogo != nil { o.VendorLogo = *vendorLogo }
		if vendorRating != nil { o.VendorRating = *vendorRating } else { o.VendorRating = 4.8 }
		if vendorReviews != nil { o.VendorReviews = *vendorReviews } else { o.VendorReviews = 1250 }
		
		offers = append(offers, o)
	}

	// If no offers, create default MegaBuy offer from product data
	if len(offers) == 0 {
		var p struct {
			PriceMin    float64
			StockStatus string
			StockQty    int
		}
		h.db.QueryRow(context.Background(), 
			`SELECT price_min, stock_status, COALESCE(stock_quantity, 0) FROM products WHERE id = $1`, 
			productID).Scan(&p.PriceMin, &p.StockStatus, &p.StockQty)
		
		shippingPrice := 2.99
		if p.PriceMin >= 49 { shippingPrice = 0 }
		
		offers = append(offers, Offer{
			ID:            uuid.New().String(),
			ProductID:     productID,
			VendorID:      "megabuy",
			VendorName:    "MegaBuy.sk",
			VendorRating:  4.8,
			VendorReviews: 1250,
			Price:         p.PriceMin,
			ShippingPrice: shippingPrice,
			DeliveryDays:  "1-2",
			StockStatus:   p.StockStatus,
			StockQuantity: p.StockQty,
			IsMegabuy:     true,
		})
	}

	return c.JSON(offers)
}

// ==================== ADMIN ENDPOINTS ====================

// GetAdminProducts - admin product listing with all fields
func (h *ProductHandler) GetAdminProducts(c *fiber.Ctx) error {
	page := c.QueryInt("page", 1)
	limit := c.QueryInt("limit", 50)
	search := c.Query("search")
	category := c.Query("category")
	status := c.Query("status")
	
	if page < 1 { page = 1 }
	if limit < 1 || limit > 100 { limit = 50 }
	offset := (page - 1) * limit

	query := `
		SELECT p.id, p.title, p.slug, p.image_url, p.ean, p.sku, p.brand,
			   p.price_min, p.price_max, p.stock_status, p.is_active,
			   c.name as category_name, p.created_at, p.updated_at
		FROM products p
		LEFT JOIN categories c ON p.category_id = c.id
		WHERE 1=1
	`
	countQuery := `SELECT COUNT(*) FROM products p LEFT JOIN categories c ON p.category_id = c.id WHERE 1=1`
	args := []interface{}{}
	argNum := 1

	if search != "" {
		searchClause := fmt.Sprintf(" AND (p.title ILIKE $%d OR p.ean ILIKE $%d OR p.sku ILIKE $%d)", argNum, argNum, argNum)
		query += searchClause
		countQuery += searchClause
		args = append(args, "%"+search+"%")
		argNum++
	}
	if category != "" {
		catClause := fmt.Sprintf(" AND p.category_id = $%d", argNum)
		query += catClause
		countQuery += catClause
		args = append(args, category)
		argNum++
	}
	if status == "active" {
		query += " AND p.is_active = true"
		countQuery += " AND p.is_active = true"
	} else if status == "inactive" {
		query += " AND p.is_active = false"
		countQuery += " AND p.is_active = false"
	}

	query += " ORDER BY p.created_at DESC"
	query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", argNum, argNum+1)
	args = append(args, limit, offset)

	rows, err := h.db.Query(context.Background(), query, args...)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	products := []map[string]interface{}{}
	for rows.Next() {
		var id, title, slug string
		var imageURL, ean, sku, brand, catName *string
		var priceMin, priceMax float64
		var stockStatus string
		var isActive bool
		var createdAt, updatedAt time.Time

		err := rows.Scan(&id, &title, &slug, &imageURL, &ean, &sku, &brand,
			&priceMin, &priceMax, &stockStatus, &isActive, &catName, &createdAt, &updatedAt)
		if err != nil {
			continue
		}

		p := map[string]interface{}{
			"id":            id,
			"title":         title,
			"slug":          slug,
			"image_url":     ptrToStr(imageURL),
			"ean":           ptrToStr(ean),
			"sku":           ptrToStr(sku),
			"brand":         ptrToStr(brand),
			"price_min":     priceMin,
			"price_max":     priceMax,
			"stock_status":  stockStatus,
			"is_active":     isActive,
			"category_name": ptrToStr(catName),
			"created_at":    createdAt,
			"updated_at":    updatedAt,
		}
		products = append(products, p)
	}

	// Count total
	var total int
	h.db.QueryRow(context.Background(), countQuery, args[:len(args)-2]...).Scan(&total)

	return c.JSON(fiber.Map{
		"items": products,
		"total": total,
		"page":  page,
		"limit": limit,
	})
}

// GetProduct - get single product by ID (admin)
func (h *ProductHandler) GetProduct(c *fiber.Ctx) error {
	id := c.Params("id")
	
	var p Product
	var catID *string
	var galleryJSON, attrsJSON []byte
	
	err := h.db.QueryRow(context.Background(), `
		SELECT id, title, slug, description, short_description,
			   ean, sku, mpn, brand, image_url, gallery_images,
			   category_id, price_min, price_max, stock_status, stock_quantity,
			   is_active, attributes, rating, review_count, feed_id, affiliate_url,
			   created_at, updated_at
		FROM products WHERE id = $1
	`, id).Scan(
		&p.ID, &p.Title, &p.Slug, &p.Description, &p.ShortDescription,
		&p.EAN, &p.SKU, &p.MPN, &p.Brand, &p.ImageURL, &galleryJSON,
		&catID, &p.PriceMin, &p.PriceMax, &p.StockStatus, &p.StockQuantity,
		&p.IsActive, &attrsJSON, &p.Rating, &p.ReviewCount, &p.FeedID, &p.AffiliateURL,
		&p.CreatedAt, &p.UpdatedAt,
	)
	
	if err == pgx.ErrNoRows {
		return c.Status(404).JSON(fiber.Map{"error": "Product not found"})
	}
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	if catID != nil { p.CategoryID = *catID }
	if galleryJSON != nil { json.Unmarshal(galleryJSON, &p.GalleryImages) }
	if attrsJSON != nil { json.Unmarshal(attrsJSON, &p.Attributes) }

	return c.JSON(p)
}

// CreateProduct - create new product with auto ES sync
func (h *ProductHandler) CreateProduct(c *fiber.Ctx) error {
	var input struct {
		Title            string            `json:"title"`
		Slug             string            `json:"slug"`
		Description      string            `json:"description"`
		ShortDescription string            `json:"short_description"`
		EAN              string            `json:"ean"`
		SKU              string            `json:"sku"`
		MPN              string            `json:"mpn"`
		Brand            string            `json:"brand"`
		ImageURL         string            `json:"image_url"`
		GalleryImages    []string          `json:"gallery_images"`
		CategoryID       string            `json:"category_id"`
		PriceMin         float64           `json:"price_min"`
		PriceMax         float64           `json:"price_max"`
		StockStatus      string            `json:"stock_status"`
		StockQuantity    int               `json:"stock_quantity"`
		IsActive         bool              `json:"is_active"`
		Attributes       map[string]string `json:"attributes"`
		AffiliateURL     string            `json:"affiliate_url"`
	}

	if err := c.BodyParser(&input); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid input"})
	}

	if input.Title == "" {
		return c.Status(400).JSON(fiber.Map{"error": "Title is required"})
	}

	// Generate slug if not provided
	if input.Slug == "" {
		input.Slug = generateSlug(input.Title)
	}

	// Default values
	if input.StockStatus == "" {
		input.StockStatus = "instock"
	}

	id := uuid.New().String()
	galleryJSON, _ := json.Marshal(input.GalleryImages)
	attrsJSON, _ := json.Marshal(input.Attributes)

	var catID *string
	if input.CategoryID != "" {
		catID = &input.CategoryID
	}

	_, err := h.db.Exec(context.Background(), `
		INSERT INTO products (id, title, slug, description, short_description,
			ean, sku, mpn, brand, image_url, gallery_images, category_id,
			price_min, price_max, stock_status, stock_quantity, is_active,
			attributes, affiliate_url, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, NOW(), NOW())
	`, id, input.Title, input.Slug, input.Description, input.ShortDescription,
		input.EAN, input.SKU, input.MPN, input.Brand, input.ImageURL, galleryJSON, catID,
		input.PriceMin, input.PriceMax, input.StockStatus, input.StockQuantity, input.IsActive,
		attrsJSON, input.AffiliateURL)

	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	// Auto-sync to Elasticsearch
	go h.syncProductToES(id)

	return c.Status(201).JSON(fiber.Map{"id": id, "slug": input.Slug})
}

// UpdateProduct - update product with auto ES sync
func (h *ProductHandler) UpdateProduct(c *fiber.Ctx) error {
	id := c.Params("id")
	
	var input map[string]interface{}
	if err := c.BodyParser(&input); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid input"})
	}

	// Build dynamic update query
	sets := []string{}
	args := []interface{}{}
	argNum := 1

	allowedFields := map[string]bool{
		"title": true, "slug": true, "description": true, "short_description": true,
		"ean": true, "sku": true, "mpn": true, "brand": true, "image_url": true,
		"category_id": true, "price_min": true, "price_max": true,
		"stock_status": true, "stock_quantity": true, "is_active": true, "affiliate_url": true,
	}

	for field, value := range input {
		if !allowedFields[field] {
			continue
		}
		sets = append(sets, fmt.Sprintf("%s = $%d", field, argNum))
		args = append(args, value)
		argNum++
	}

	// Handle special fields
	if gallery, ok := input["gallery_images"]; ok {
		galleryJSON, _ := json.Marshal(gallery)
		sets = append(sets, fmt.Sprintf("gallery_images = $%d", argNum))
		args = append(args, galleryJSON)
		argNum++
	}
	if attrs, ok := input["attributes"]; ok {
		attrsJSON, _ := json.Marshal(attrs)
		sets = append(sets, fmt.Sprintf("attributes = $%d", argNum))
		args = append(args, attrsJSON)
		argNum++
	}

	if len(sets) == 0 {
		return c.Status(400).JSON(fiber.Map{"error": "No fields to update"})
	}

	sets = append(sets, "updated_at = NOW()")
	args = append(args, id)

	query := fmt.Sprintf("UPDATE products SET %s WHERE id = $%d", strings.Join(sets, ", "), argNum)
	result, err := h.db.Exec(context.Background(), query, args...)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	if result.RowsAffected() == 0 {
		return c.Status(404).JSON(fiber.Map{"error": "Product not found"})
	}

	// Auto-sync to Elasticsearch
	go h.syncProductToES(id)

	return c.JSON(fiber.Map{"success": true})
}

// DeleteProduct - delete product with ES cleanup
func (h *ProductHandler) DeleteProduct(c *fiber.Ctx) error {
	id := c.Params("id")
	
	result, err := h.db.Exec(context.Background(), "DELETE FROM products WHERE id = $1", id)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	if result.RowsAffected() == 0 {
		return c.Status(404).JSON(fiber.Map{"error": "Product not found"})
	}

	// Remove from Elasticsearch
	go h.deleteProductFromES(id)

	return c.JSON(fiber.Map{"success": true})
}

// BulkUpdateProducts - bulk operations
func (h *ProductHandler) BulkUpdateProducts(c *fiber.Ctx) error {
	var input struct {
		IDs    []string `json:"ids"`
		Action string   `json:"action"` // activate, deactivate, delete
	}

	if err := c.BodyParser(&input); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid input"})
	}

	if len(input.IDs) == 0 {
		return c.Status(400).JSON(fiber.Map{"error": "No IDs provided"})
	}

	var err error
	switch input.Action {
	case "activate":
		_, err = h.db.Exec(context.Background(), 
			"UPDATE products SET is_active = true, updated_at = NOW() WHERE id = ANY($1)", input.IDs)
	case "deactivate":
		_, err = h.db.Exec(context.Background(), 
			"UPDATE products SET is_active = false, updated_at = NOW() WHERE id = ANY($1)", input.IDs)
	case "delete":
		_, err = h.db.Exec(context.Background(), "DELETE FROM products WHERE id = ANY($1)", input.IDs)
	default:
		return c.Status(400).JSON(fiber.Map{"error": "Invalid action"})
	}

	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	// Sync all affected products to ES
	go func() {
		for _, id := range input.IDs {
			if input.Action == "delete" {
				h.deleteProductFromES(id)
			} else {
				h.syncProductToES(id)
			}
		}
	}()

	return c.JSON(fiber.Map{"success": true, "affected": len(input.IDs)})
}

// ==================== ELASTICSEARCH SYNC ====================

func (h *ProductHandler) syncProductToES(productID string) {
	if h.es == nil {
		return
	}

	var p Product
	var catName, catSlug *string
	var galleryJSON, attrsJSON []byte

	err := h.db.QueryRow(context.Background(), `
		SELECT p.id, p.title, p.slug, p.description, p.short_description,
			   p.ean, p.sku, p.mpn, p.brand, p.image_url, p.gallery_images,
			   c.name, c.slug, p.price_min, p.price_max,
			   p.stock_status, p.stock_quantity, p.is_active, p.attributes,
			   p.rating, p.review_count, p.created_at, p.updated_at
		FROM products p
		LEFT JOIN categories c ON p.category_id = c.id
		WHERE p.id = $1
	`, productID).Scan(
		&p.ID, &p.Title, &p.Slug, &p.Description, &p.ShortDescription,
		&p.EAN, &p.SKU, &p.MPN, &p.Brand, &p.ImageURL, &galleryJSON,
		&catName, &catSlug, &p.PriceMin, &p.PriceMax,
		&p.StockStatus, &p.StockQuantity, &p.IsActive, &attrsJSON,
		&p.Rating, &p.ReviewCount, &p.CreatedAt, &p.UpdatedAt,
	)

	if err != nil {
		return
	}

	if catName != nil { p.CategoryName = *catName }
	if catSlug != nil { p.CategorySlug = *catSlug }
	if galleryJSON != nil { json.Unmarshal(galleryJSON, &p.GalleryImages) }
	if attrsJSON != nil { json.Unmarshal(attrsJSON, &p.Attributes) }

	docJSON, _ := json.Marshal(p)

	req := esapi.IndexRequest{
		Index:      "products",
		DocumentID: p.ID,
		Body:       strings.NewReader(string(docJSON)),
		Refresh:    "true",
	}

	req.Do(context.Background(), h.es)
}

func (h *ProductHandler) deleteProductFromES(productID string) {
	if h.es == nil {
		return
	}

	req := esapi.DeleteRequest{
		Index:      "products",
		DocumentID: productID,
		Refresh:    "true",
	}

	req.Do(context.Background(), h.es)
}

// SyncAllToES - sync all products to Elasticsearch
func (h *ProductHandler) SyncAllToES(c *fiber.Ctx) error {
	if h.es == nil {
		return c.Status(500).JSON(fiber.Map{"error": "Elasticsearch not configured"})
	}

	rows, err := h.db.Query(context.Background(), "SELECT id FROM products WHERE is_active = true")
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}

	// Sync in background
	go func() {
		for _, id := range ids {
			h.syncProductToES(id)
			time.Sleep(10 * time.Millisecond) // Rate limiting
		}
	}()

	return c.JSON(fiber.Map{"message": "Sync started", "count": len(ids)})
}

// ==================== HELPERS ====================

func generateSlug(title string) string {
	slug := strings.ToLower(title)
	slug = strings.ReplaceAll(slug, " ", "-")
	// Remove special characters
	var result strings.Builder
	for _, r := range slug {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			result.WriteRune(r)
		}
	}
	return result.String() + "-" + uuid.New().String()[:8]
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok && v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getFloat(m map[string]interface{}, key string) float64 {
	if v, ok := m[key]; ok && v != nil {
		if f, ok := v.(float64); ok {
			return f
		}
	}
	return 0
}

func getInt(m map[string]interface{}, key string) int {
	if v, ok := m[key]; ok && v != nil {
		if f, ok := v.(float64); ok {
			return int(f)
		}
	}
	return 0
}

func getBool(m map[string]interface{}, key string) bool {
	if v, ok := m[key]; ok && v != nil {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func extractBuckets(aggs map[string]interface{}, key string) []map[string]interface{} {
	if agg, ok := aggs[key].(map[string]interface{}); ok {
		if buckets, ok := agg["buckets"].([]interface{}); ok {
			result := []map[string]interface{}{}
			for _, b := range buckets {
				if bucket, ok := b.(map[string]interface{}); ok {
					result = append(result, bucket)
				}
			}
			return result
		}
	}
	return nil
}

func ptrToStr(s *string) string {
	if s != nil {
		return *s
	}
	return ""
}
