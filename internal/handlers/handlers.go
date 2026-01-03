package handlers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	db *database.DB
	es *elasticsearch.Client
}

func New(db *database.DB) *Handlers {
	es := elasticsearch.New()
	es.CreateIndex()
	return &Handlers{db: db, es: es}
}

func makeSlug(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	r, _, _ := transform.String(t, strings.ToLower(s))
	result := strings.Map(func(c rune) rune {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' { return c }
		if c == ' ' || c == '-' { return '-' }
		return -1
	}, r)
	for strings.Contains(result, "--") { result = strings.ReplaceAll(result, "--", "-") }
	return strings.Trim(result, "-")
}

// ========== SEARCH API (Elasticsearch) ==========

func (h *Handlers) Search(c *fiber.Ctx) error {
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
		if end > len(products) { end = len(products) }
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
	if c.Query("q") != "" {
		return h.Search(c)
	}

	page := c.QueryInt("page", 1)
	limit := c.QueryInt("limit", 20)
	if page < 1 { page = 1 }
	offset := (page - 1) * limit
	ctx := context.Background()

	var total int
	h.db.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM products WHERE is_active=true").Scan(&total)

	rows, _ := h.db.Pool.Query(ctx, `
		SELECT p.id, p.title, p.slug, COALESCE(p.short_description,''), COALESCE(p.image_url,''), 
		       p.price_min, p.price_max, COALESCE(p.stock_status,'instock'), COALESCE(c.name,''), COALESCE(c.slug,'')
		FROM products p LEFT JOIN categories c ON p.category_id = c.id
		WHERE p.is_active=true ORDER BY p.created_at DESC LIMIT $1 OFFSET $2
	`, limit, offset)
	defer rows.Close()

	var products []fiber.Map
	for rows.Next() {
		var id, title, slug, shortDesc, img, stockStatus, catName, catSlug string
		var pmin, pmax float64
		rows.Scan(&id, &title, &slug, &shortDesc, &img, &pmin, &pmax, &stockStatus, &catName, &catSlug)
		products = append(products, fiber.Map{
			"id": id, "title": title, "slug": slug, "short_description": shortDesc,
			"image_url": img, "price_min": pmin, "price_max": pmax, "stock_status": stockStatus,
			"category_name": catName, "category_slug": catSlug,
		})
	}
	if products == nil { products = []fiber.Map{} }

	return c.JSON(fiber.Map{"success": true, "data": fiber.Map{
		"items": products, "total": total, "page": page, "limit": limit, "total_pages": (total + limit - 1) / limit,
	}})
}

func (h *Handlers) GetFeaturedProducts(c *fiber.Ctx) error {
	limit := c.QueryInt("limit", 8)
	ctx := context.Background()
	rows, _ := h.db.Pool.Query(ctx, `
		SELECT p.id, p.title, p.slug, COALESCE(p.image_url,''), p.price_min, p.price_max, COALESCE(c.name,''), COALESCE(c.slug,'')
		FROM products p LEFT JOIN categories c ON p.category_id = c.id
		WHERE p.is_active=true ORDER BY p.is_featured DESC, p.created_at DESC LIMIT $1
	`, limit)
	defer rows.Close()
	var products []fiber.Map
	for rows.Next() {
		var id, title, slug, img, catName, catSlug string
		var pmin, pmax float64
		rows.Scan(&id, &title, &slug, &img, &pmin, &pmax, &catName, &catSlug)
		products = append(products, fiber.Map{"id": id, "title": title, "slug": slug, "image_url": img, "price_min": pmin, "price_max": pmax, "category_name": catName, "category_slug": catSlug})
	}
	if products == nil { products = []fiber.Map{} }
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
	if err != nil { return c.Status(404).JSON(fiber.Map{"success": false, "error": "Product not found"}) }

	// Get images
	imgRows, _ := h.db.Pool.Query(ctx, `SELECT url FROM product_images WHERE product_id = $1::uuid ORDER BY position`, id)
	defer imgRows.Close()
	var images []string
	for imgRows.Next() {
		var imgURL string
		imgRows.Scan(&imgURL)
		images = append(images, imgURL)
	}

	return c.JSON(fiber.Map{"success": true, "data": fiber.Map{
		"id": id, "title": title, "slug": pslug, "description": desc, "short_description": shortDesc,
		"ean": ean, "sku": sku, "mpn": mpn, "brand": brand, "image_url": img, "images": images,
		"stock_status": stockStatus, "category_id": catID, "category_name": catName, "category_slug": catSlug,
		"affiliate_url": affiliateURL, "price_min": priceMin, "price_max": priceMax, "is_active": isActive, "created_at": createdAt,
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
	if cats == nil { cats = []fiber.Map{} }
	return c.JSON(fiber.Map{"success": true, "data": cats})
}

func (h *Handlers) GetCategoriesTree(c *fiber.Ctx) error {
	ctx := context.Background()
	rows, _ := h.db.Pool.Query(ctx, `SELECT id, COALESCE(parent_id::text,''), name, slug, COALESCE(icon,''), product_count FROM categories WHERE is_active=true ORDER BY sort_order, name`)
	defer rows.Close()

	type Cat struct {
		ID string `json:"id"`
		ParentID string `json:"parent_id,omitempty"`
		Name string `json:"name"`
		Slug string `json:"slug"`
		Icon string `json:"icon,omitempty"`
		ProductCount int `json:"product_count"`
		Children []*Cat `json:"children,omitempty"`
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
	if roots == nil { roots = []*Cat{} }
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
	if cats == nil { cats = []fiber.Map{} }
	return c.JSON(fiber.Map{"success": true, "data": cats})
}

func (h *Handlers) GetCategoryBySlug(c *fiber.Ctx) error {
	slug := c.Params("slug")
	ctx := context.Background()
	var id, parentID, name, cslug, desc, icon string
	var productCount int
	err := h.db.Pool.QueryRow(ctx, `SELECT id, COALESCE(parent_id::text,''), name, slug, COALESCE(description,''), COALESCE(icon,''), product_count FROM categories WHERE slug = $1 AND is_active=true`, slug).Scan(&id, &parentID, &name, &cslug, &desc, &icon, &productCount)
	if err != nil { return c.Status(404).JSON(fiber.Map{"success": false, "error": "Category not found"}) }
	return c.JSON(fiber.Map{"success": true, "data": fiber.Map{"id": id, "parent_id": parentID, "name": name, "slug": cslug, "description": desc, "icon": icon, "product_count": productCount}})
}

func (h *Handlers) GetProductsByCategory(c *fiber.Ctx) error {
	slug := c.Params("slug")
	ctx := context.Background()
	var categoryID string
	err := h.db.Pool.QueryRow(ctx, "SELECT id FROM categories WHERE slug = $1", slug).Scan(&categoryID)
	if err != nil { return c.Status(404).JSON(fiber.Map{"success": false, "error": "Category not found"}) }
	
	rows, _ := h.db.Pool.Query(ctx, `SELECT p.id, p.title, p.slug, COALESCE(p.image_url,''), p.price_min, p.price_max FROM products p WHERE p.category_id = $1::uuid AND p.is_active=true ORDER BY p.created_at DESC`, categoryID)
	defer rows.Close()
	var products []fiber.Map
	for rows.Next() {
		var id, title, slug, img string
		var pmin, pmax float64
		rows.Scan(&id, &title, &slug, &img, &pmin, &pmax)
		products = append(products, fiber.Map{"id": id, "title": title, "slug": slug, "image_url": img, "price_min": pmin, "price_max": pmax})
	}
	if products == nil { products = []fiber.Map{} }
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
	if priceMin >= 49 { shippingPrice = 0 }
	
	return c.JSON(fiber.Map{"success": true, "data": []fiber.Map{{
		"id": "default", "vendor_id": "megabuy", "vendor_name": "MegaBuy.sk",
		"vendor_logo": "", "vendor_rating": 4.8, "vendor_reviews": 1250,
		"price": priceMin, "shipping_price": shippingPrice, "delivery_days": "1-2",
		"stock_status": stockStatus, "stock_quantity": 10, "is_megabuy": true, "affiliate_url": affiliateURL,
	}}})
}

// ========== ADMIN API ==========

func (h *Handlers) AdminProducts(c *fiber.Ctx) error {
	page := c.QueryInt("page", 1)
	limit := c.QueryInt("limit", 20)
	search := c.Query("search")
	if page < 1 { page = 1 }
	offset := (page - 1) * limit
	ctx := context.Background()

	var total int
	if search != "" {
		h.db.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM products WHERE title ILIKE $1 OR ean ILIKE $1", "%"+search+"%").Scan(&total)
	} else {
		h.db.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM products").Scan(&total)
	}

	query := `SELECT p.id, p.title, p.slug, COALESCE(p.ean,''), COALESCE(p.sku,''), COALESCE(p.image_url,''), p.price_min, p.price_max, p.is_active, COALESCE(p.stock_status,'instock'), COALESCE(c.name,''), p.created_at FROM products p LEFT JOIN categories c ON p.category_id = c.id`
	var rows interface{}
	var err error
	if search != "" {
		query += " WHERE p.title ILIKE $3 OR p.ean ILIKE $3 ORDER BY p.created_at DESC LIMIT $1 OFFSET $2"
		rows, err = h.db.Pool.Query(ctx, query, limit, offset, "%"+search+"%")
	} else {
		query += " ORDER BY p.created_at DESC LIMIT $1 OFFSET $2"
		rows, err = h.db.Pool.Query(ctx, query, limit, offset)
	}
	if err != nil { return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error()}) }

	pgRows := rows.(interface{ Close(); Next() bool; Scan(...interface{}) error })
	defer pgRows.Close()

	var products []fiber.Map
	for pgRows.Next() {
		var id, title, slug, ean, sku, img, stockStatus, catName string
		var pmin, pmax float64
		var isActive bool
		var createdAt time.Time
		pgRows.Scan(&id, &title, &slug, &ean, &sku, &img, &pmin, &pmax, &isActive, &stockStatus, &catName, &createdAt)
		products = append(products, fiber.Map{"id": id, "title": title, "slug": slug, "ean": ean, "sku": sku, "image_url": img, "price_min": pmin, "price_max": pmax, "is_active": isActive, "stock_status": stockStatus, "category_name": catName, "created_at": createdAt})
	}
	if products == nil { products = []fiber.Map{} }
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
	if err != nil { return c.Status(404).JSON(fiber.Map{"success": false, "error": "Product not found"}) }

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
		Title string `json:"title"`
		Slug string `json:"slug"`
		Description string `json:"description"`
		ShortDescription string `json:"short_description"`
		EAN string `json:"ean"`
		SKU string `json:"sku"`
		MPN string `json:"mpn"`
		Brand string `json:"brand_name"`
		CategoryID string `json:"category_id"`
		ImageURL string `json:"image_url"`
		PriceMin float64 `json:"price_min"`
		PriceMax float64 `json:"price_max"`
		StockStatus string `json:"stock_status"`
		IsActive bool `json:"is_active"`
	}
	if err := c.BodyParser(&input); err != nil { return c.Status(400).JSON(fiber.Map{"success": false, "error": "Invalid request"}) }
	if input.Title == "" { return c.Status(400).JSON(fiber.Map{"success": false, "error": "Title required"}) }
	if input.Slug == "" { input.Slug = makeSlug(input.Title) }
	if input.StockStatus == "" { input.StockStatus = "instock" }
	if input.PriceMax == 0 && input.PriceMin > 0 { input.PriceMax = input.PriceMin }

	ctx := context.Background()
	productID := uuid.New()
	var catID interface{} = nil
	if input.CategoryID != "" { catID = input.CategoryID }

	_, err := h.db.Pool.Exec(ctx, `INSERT INTO products (id, category_id, title, slug, description, short_description, ean, sku, mpn, brand, image_url, price_min, price_max, stock_status, is_active, created_at, updated_at) VALUES ($1, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, NOW(), NOW())`, productID, catID, input.Title, input.Slug, input.Description, input.ShortDescription, input.EAN, input.SKU, input.MPN, input.Brand, input.ImageURL, input.PriceMin, input.PriceMax, input.StockStatus, input.IsActive)
	if err != nil { return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error()}) }

	if input.CategoryID != "" { h.db.Pool.Exec(ctx, `UPDATE categories SET product_count = (SELECT COUNT(*) FROM products WHERE category_id = $1::uuid AND is_active=true) WHERE id = $1::uuid`, input.CategoryID) }

	return c.Status(201).JSON(fiber.Map{"success": true, "data": fiber.Map{"id": productID.String(), "slug": input.Slug}})
}

func (h *Handlers) AdminUpdateProduct(c *fiber.Ctx) error {
	productID := c.Params("id")
	var input struct {
		Title string `json:"title"`
		Slug string `json:"slug"`
		Description string `json:"description"`
		ShortDescription string `json:"short_description"`
		EAN string `json:"ean"`
		SKU string `json:"sku"`
		MPN string `json:"mpn"`
		Brand string `json:"brand_name"`
		CategoryID string `json:"category_id"`
		ImageURL string `json:"image_url"`
		PriceMin float64 `json:"price_min"`
		PriceMax float64 `json:"price_max"`
		StockStatus string `json:"stock_status"`
		IsActive bool `json:"is_active"`
	}
	if err := c.BodyParser(&input); err != nil { return c.Status(400).JSON(fiber.Map{"success": false, "error": "Invalid request"}) }

	ctx := context.Background()
	var catID interface{} = nil
	if input.CategoryID != "" { catID = input.CategoryID }

	_, err := h.db.Pool.Exec(ctx, `UPDATE products SET category_id = $2::uuid, title = COALESCE(NULLIF($3,''), title), slug = COALESCE(NULLIF($4,''), slug), description = $5, short_description = $6, ean = $7, sku = $8, mpn = $9, brand = $10, image_url = $11, price_min = $12, price_max = $13, stock_status = $14, is_active = $15, updated_at = NOW() WHERE id = $1::uuid`, productID, catID, input.Title, input.Slug, input.Description, input.ShortDescription, input.EAN, input.SKU, input.MPN, input.Brand, input.ImageURL, input.PriceMin, input.PriceMax, input.StockStatus, input.IsActive)
	if err != nil { return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error()}) }

	return c.JSON(fiber.Map{"success": true, "message": "Product updated"})
}

func (h *Handlers) AdminDeleteProduct(c *fiber.Ctx) error {
	productID := c.Params("id")
	ctx := context.Background()
	h.db.Pool.Exec(ctx, "DELETE FROM product_images WHERE product_id = $1::uuid", productID)
	h.db.Pool.Exec(ctx, "DELETE FROM product_attributes WHERE product_id = $1::uuid", productID)
	_, err := h.db.Pool.Exec(ctx, "DELETE FROM products WHERE id = $1::uuid", productID)
	if err != nil { return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error()}) }
	h.es.DeleteProduct(productID)
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
			h.es.DeleteProduct(id)
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
	if cats == nil { cats = []fiber.Map{} }
	return c.JSON(fiber.Map{"success": true, "data": cats})
}

func (h *Handlers) AdminCreateCategory(c *fiber.Ctx) error {
	var input struct {
		ParentID string `json:"parent_id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
		Description string `json:"description"`
		Icon string `json:"icon"`
	}
	if err := c.BodyParser(&input); err != nil { return c.Status(400).JSON(fiber.Map{"success": false, "error": "Invalid request"}) }
	if input.Name == "" { return c.Status(400).JSON(fiber.Map{"success": false, "error": "Name required"}) }
	if input.Slug == "" { input.Slug = makeSlug(input.Name) }

	ctx := context.Background()
	id := uuid.New()
	var err error
	if input.ParentID != "" {
		_, err = h.db.Pool.Exec(ctx, `INSERT INTO categories (id, parent_id, name, slug, description, icon, is_active, created_at, updated_at) VALUES ($1, $2::uuid, $3, $4, $5, $6, true, NOW(), NOW())`, id, input.ParentID, input.Name, input.Slug, input.Description, input.Icon)
	} else {
		_, err = h.db.Pool.Exec(ctx, `INSERT INTO categories (id, name, slug, description, icon, is_active, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, true, NOW(), NOW())`, id, input.Name, input.Slug, input.Description, input.Icon)
	}
	if err != nil { return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error()}) }
	return c.Status(201).JSON(fiber.Map{"success": true, "data": fiber.Map{"id": id.String(), "slug": input.Slug}})
}

func (h *Handlers) AdminUpdateCategory(c *fiber.Ctx) error {
	categoryID := c.Params("id")
	var input struct {
		ParentID string `json:"parent_id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
		Description string `json:"description"`
		Icon string `json:"icon"`
		IsActive bool `json:"is_active"`
	}
	if err := c.BodyParser(&input); err != nil { return c.Status(400).JSON(fiber.Map{"success": false, "error": "Invalid request"}) }

	ctx := context.Background()
	var err error
	if input.ParentID != "" {
		_, err = h.db.Pool.Exec(ctx, `UPDATE categories SET parent_id = $2::uuid, name = COALESCE(NULLIF($3,''), name), slug = COALESCE(NULLIF($4,''), slug), description = $5, icon = $6, is_active = $7, updated_at = NOW() WHERE id = $1::uuid`, categoryID, input.ParentID, input.Name, input.Slug, input.Description, input.Icon, input.IsActive)
	} else {
		_, err = h.db.Pool.Exec(ctx, `UPDATE categories SET parent_id = NULL, name = COALESCE(NULLIF($2,''), name), slug = COALESCE(NULLIF($3,''), slug), description = $4, icon = $5, is_active = $6, updated_at = NOW() WHERE id = $1::uuid`, categoryID, input.Name, input.Slug, input.Description, input.Icon, input.IsActive)
	}
	if err != nil { return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error()}) }
	return c.JSON(fiber.Map{"success": true, "message": "Category updated"})
}

func (h *Handlers) AdminDeleteCategory(c *fiber.Ctx) error {
	categoryID := c.Params("id")
	ctx := context.Background()
	h.db.Pool.Exec(ctx, "UPDATE categories SET parent_id = NULL WHERE parent_id = $1::uuid", categoryID)
	_, err := h.db.Pool.Exec(ctx, "DELETE FROM categories WHERE id = $1::uuid", categoryID)
	if err != nil { return c.Status(500).JSON(fiber.Map{"success": false, "error": err.Error()}) }
	return c.JSON(fiber.Map{"success": true, "message": "Category deleted"})
}

func (h *Handlers) UploadImage(c *fiber.Ctx) error {
	file, err := c.FormFile("file")
	if err != nil { return c.Status(400).JSON(fiber.Map{"success": false, "error": "No file uploaded"}) }
	uploadDir := "./uploads"
	os.MkdirAll(uploadDir, 0755)
	ext := filepath.Ext(file.Filename)
	filename := fmt.Sprintf("%s%s", uuid.New().String(), ext)
	fpath := fmt.Sprintf("%s/%s", uploadDir, filename)
	if err := c.SaveFile(file, fpath); err != nil { return c.Status(500).JSON(fiber.Map{"success": false, "error": "Failed to save file"}) }
	baseURL := c.BaseURL()
	url := fmt.Sprintf("%s/uploads/%s", baseURL, filename)
	return c.JSON(fiber.Map{"success": true, "data": fiber.Map{"url": url, "filename": filename}})
}
