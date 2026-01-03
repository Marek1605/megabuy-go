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
	app := fiber.New(fiber.Config{AppName: "MegaBuy API", BodyLimit: 50 * 1024 * 1024})
	app.Use(logger.New())
	app.Use(cors.New(cors.Config{AllowOrigins: "*", AllowMethods: "GET,POST,PUT,DELETE,OPTIONS", AllowHeaders: "Origin,Content-Type,Accept,Authorization"}))
	app.Static("/uploads", "./uploads")
	app.Get("/health", func(c *fiber.Ctx) error { return c.JSON(fiber.Map{"status": "ok"}) })

	api := app.Group("/api/v1")
	api.Get("/products", h.GetProducts)
	api.Get("/categories", h.GetCategories)
	api.Get("/stats", h.GetStats)

	admin := api.Group("/admin")
	admin.Get("/dashboard", h.AdminDashboard)
	admin.Get("/products", h.AdminProducts)
	admin.Post("/products", h.AdminCreateProduct)
	admin.Get("/categories", h.AdminCategories)
	admin.Post("/categories", h.AdminCreateCategory)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	fmt.Printf("MegaBuy API starting on port %s\n", port)
	log.Fatal(app.Listen(":" + port))
}
