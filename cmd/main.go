package main

import (
	"fmt"
	"log"
	"os"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/joho/godotenv"

	"megabuy-go/internal/database"
	"megabuy-go/internal/handlers"
)

func main() {
	godotenv.Load()

	db, err := database.New()
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	if os.Getenv("RUN_MIGRATIONS") == "true" {
		if err := db.RunMigrations("./migrations/001_init.sql"); err != nil {
			log.Printf("Migration warning: %v", err)
		}
	}

	h := handlers.New(db)

	app := fiber.New(fiber.Config{
		AppName:   "MegaBuy API",
		BodyLimit: 50 * 1024 * 1024,
	})

	app.Use(logger.New())
	app.Use(cors.New(cors.Config{
		AllowOrigins: "*",
		AllowMethods: "GET,POST,PUT,DELETE,PATCH,OPTIONS",
		AllowHeaders: "Origin,Content-Type,Accept,Authorization",
	}))

	app.Static("/uploads", "./uploads")

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})

	// API v1 routes
	api := app.Group("/api/v1")

	// Public routes
	api.Get("/search", h.Search)
	api.Get("/products", h.GetProducts)
	api.Get("/products/featured", h.GetFeaturedProducts)
	api.Get("/products/slug/:slug", h.GetProductBySlug)
	api.Get("/products/:id/offers", h.GetProductOffers)
	api.Get("/categories", h.GetCategories)
	api.Get("/categories/tree", h.GetCategoriesTree)
	api.Get("/categories/flat", h.GetCategoriesFlat)
	api.Get("/categories/slug/:slug", h.GetCategoryBySlug)
	api.Get("/categories/:slug/products", h.GetProductsByCategory)
	api.Get("/stats", h.GetStats)

	// Attribute stats (public for filtering)
	api.Get("/attributes/stats", h.GetAttributeStats)

	// Admin routes
	admin := api.Group("/admin")
	admin.Get("/dashboard", h.AdminDashboard)
	admin.Post("/sync-elasticsearch", h.SyncToElasticsearch)
	
	// Filter settings
	admin.Get("/filter-settings", h.GetFilterSettings)
	admin.Put("/filter-settings", h.UpdateFilterSettings)
	
	// Products
	admin.Get("/products", h.AdminProducts)
	admin.Delete("/products/all", h.DeleteAllProducts)
	admin.Post("/products/bulk", h.BulkDeleteProducts)
	admin.Get("/products/:id", h.AdminGetProduct)
	admin.Post("/products", h.AdminCreateProduct)
	admin.Put("/products/:id", h.AdminUpdateProduct)
	admin.Delete("/products/:id", h.AdminDeleteProduct)
	// Categories
	admin.Get("/categories", h.AdminCategories)
	admin.Post("/categories", h.AdminCreateCategory)
	admin.Put("/categories/:id", h.AdminUpdateCategory)
	admin.Delete("/categories/:id", h.AdminDeleteCategory)
	
	// Upload
	admin.Post("/upload", h.UploadImage)
	
	// Feeds
	admin.Get("/feeds", h.GetFeeds)
	admin.Post("/feeds", h.CreateFeed)
	admin.Post("/feeds/preview", h.PreviewFeed)
	admin.Put("/feeds/:id", h.UpdateFeed)
	admin.Delete("/feeds/:id", h.DeleteFeed)
	admin.Post("/feeds/:id/import", h.StartImport)
	admin.Get("/feeds/:id/progress", h.GetImportProgress)

	// Legacy routes without /api/v1 prefix (frontend compatibility)
	app.Get("/products", h.GetProducts)
	app.Get("/categories", h.GetCategories)
	app.Get("/categories/tree", h.GetCategoriesTree)
	app.Get("/categories/flat", h.GetCategoriesFlat)
	app.Get("/admin/products", h.AdminProducts)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	fmt.Printf("?? MegaBuy API starting on port %s\n", port)
	fmt.Printf("?? Elasticsearch: %s\n", os.Getenv("ELASTICSEARCH_URL"))
	log.Fatal(app.Listen(":" + port))
}
