package main

import (
	"context"
	"log"
	"os"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/jackc/pgx/v5/pgxpool"

	"megabuy-go/internal/handlers"
)

func main() {
	// Database connection
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"
	}

	db, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}
	defer db.Close()

	// Test DB connection
	if err := db.Ping(context.Background()); err != nil {
		log.Fatal("Database ping failed:", err)
	}
	log.Println("Connected to PostgreSQL")

	// Elasticsearch connection
	esURL := os.Getenv("ELASTICSEARCH_URL")
	if esURL == "" {
		esURL = "http://10.0.2.2:9200" // Default for Coolify internal network
	}

	var es *elasticsearch.Client
	es, err = elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{esURL},
	})
	if err != nil {
		log.Println("Warning: Elasticsearch connection failed:", err)
		es = nil
	} else {
		res, err := es.Info()
		if err != nil || res.IsError() {
			log.Println("Warning: Elasticsearch not available")
			es = nil
		} else {
			log.Println("Connected to Elasticsearch")
			res.Body.Close()
		}
	}

	// Initialize handlers
	productHandler := handlers.NewProductHandler(db, es)
	categoryHandler := handlers.NewCategoryHandler(db)
	feedHandler := handlers.NewFeedHandler(db, es)
	uploadHandler := handlers.NewUploadHandler()

	// Fiber app
	app := fiber.New(fiber.Config{
		BodyLimit: 50 * 1024 * 1024, // 50MB for image uploads
	})

	// Middleware
	app.Use(logger.New())
	app.Use(cors.New(cors.Config{
		AllowOrigins: "*",
		AllowHeaders: "Origin, Content-Type, Accept, Authorization",
		AllowMethods: "GET, POST, PUT, DELETE, OPTIONS, PATCH",
	}))

	// Health check
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status":   "ok",
			"database": "connected",
			"elasticsearch": func() string {
				if es != nil {
					return "connected"
				}
				return "not available"
			}(),
		})
	})

	// API routes
	api := app.Group("/api/v1")

	// ==================== PUBLIC ROUTES ====================
	
	// Products - public
	api.Get("/products", productHandler.GetProducts)
	api.Get("/products/slug/:slug", productHandler.GetProductBySlug)
	api.Get("/products/:id/offers", productHandler.GetProductOffers)
	
	// Categories - public
	api.Get("/categories", categoryHandler.GetCategories)
	api.Get("/categories/tree", categoryHandler.GetCategoryTree)
	api.Get("/categories/flat", categoryHandler.GetCategoriesFlat)
	api.Get("/categories/slug/:slug", categoryHandler.GetCategoryBySlug)

	// Search
	api.Get("/search", productHandler.GetProducts) // Same as products with search param

	// ==================== ADMIN ROUTES ====================
	admin := api.Group("/admin")

	// Products - admin
	admin.Get("/products", productHandler.GetAdminProducts)
	admin.Get("/products/:id", productHandler.GetProduct)
	admin.Post("/products", productHandler.CreateProduct)
	admin.Put("/products/:id", productHandler.UpdateProduct)
	admin.Delete("/products/:id", productHandler.DeleteProduct)
	admin.Post("/products/bulk", productHandler.BulkUpdateProducts)
	admin.Post("/products/sync-es", productHandler.SyncAllToES)

	// Categories - admin
	admin.Get("/categories", categoryHandler.GetCategories)
	admin.Get("/categories/:id", categoryHandler.GetCategory)
	admin.Post("/categories", categoryHandler.CreateCategory)
	admin.Put("/categories/:id", categoryHandler.UpdateCategory)
	admin.Delete("/categories/:id", categoryHandler.DeleteCategory)

	// Feeds - admin
	admin.Get("/feeds", feedHandler.GetFeeds)
	admin.Post("/feeds", feedHandler.CreateFeed)
	admin.Post("/feeds/preview", feedHandler.PreviewFeed)
	admin.Put("/feeds/:id", feedHandler.UpdateFeed)
	admin.Delete("/feeds/:id", feedHandler.DeleteFeed)
	admin.Post("/feeds/:id/import", feedHandler.StartImport)
	admin.Get("/feeds/:id/progress", feedHandler.GetImportProgress)

	// Upload
	admin.Post("/upload", uploadHandler.UploadImage)
	admin.Post("/upload/multiple", uploadHandler.UploadMultiple)

	// Start server
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Server starting on port %s", port)
	log.Fatal(app.Listen(":" + port))
}
