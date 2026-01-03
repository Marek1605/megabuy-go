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
		BodyLimit: 50 * 1024 * 1024, // 50MB
	})

	app.Use(logger.New())
	app.Use(cors.New(cors.Config{
		AllowOrigins: "*",
		AllowMethods: "GET,POST,PUT,DELETE,PATCH,OPTIONS",
		AllowHeaders: "Origin,Content-Type,Accept,Authorization",
	}))

	// Static files for uploads
	app.Static("/uploads", "./uploads")

	// Health check
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})

	// ========== PUBLIC API ==========
	api := app.Group("/api/v1")

	// Search (Elasticsearch)
	api.Get("/search", h.Search)

	// Products
	api.Get("/products", h.GetProducts)
	api.Get("/products/featured", h.GetFeaturedProducts)
	api.Get("/products/:slug", h.GetProductBySlug)

	// Categories
	api.Get("/categories", h.GetCategories)
	api.Get("/categories/:slug", h.GetCategoryBySlug)
	api.Get("/categories/:slug/products", h.GetProductsByCategory)

	// Stats
	api.Get("/stats", h.GetStats)

	// ========== ADMIN API ==========
	admin := api.Group("/admin")

	// Dashboard
	admin.Get("/dashboard", h.AdminDashboard)

	// Elasticsearch Sync
	admin.Post("/sync-elasticsearch", h.SyncToElasticsearch)

	// Products
	admin.Get("/products", h.AdminProducts)
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

	// ========== START ==========
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	fmt.Printf("🚀 MegaBuy API starting on port %s\n", port)
	fmt.Printf("📊 Elasticsearch: %s\n", os.Getenv("ELASTICSEARCH_URL"))
	log.Fatal(app.Listen(":" + port))
}
