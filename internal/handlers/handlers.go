package handlers

import (
	"context"
	"strings"
	"unicode"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
	"megabuy-go/internal/database"
)

type Handlers struct{ db *database.DB }

func New(db *database.DB) *Handlers { return &Handlers{db: db} }

func makeSlug(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	r, _, _ := transform.String(t, strings.ToLower(s))
	return strings.Trim(strings.Map(func(c rune) rune {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' { return c }
		if c == ' ' || c == '-' { return '-' }
		return -1
	}, r), "-")
}

func (h *Handlers) GetProducts(c *fiber.Ctx) error {
	rows, _ := h.db.Pool.Query(context.Background(), "SELECT id,title,slug,COALESCE(image_url,''),price_min,price_max FROM products WHERE is_active=true LIMIT 50")
	defer rows.Close()
	var products []fiber.Map
	for rows.Next() {
		var id,title,slug,img string; var pmin,pmax float64
		rows.Scan(&id,&title,&slug,&img,&pmin,&pmax)
		products = append(products, fiber.Map{"id":id,"title":title,"slug":slug,"image_url":img,"price_min":pmin,"price_max":pmax})
	}
	if products == nil { products = []fiber.Map{} }
	return c.JSON(fiber.Map{"success":true,"data":fiber.Map{"items":products,"total":len(products)}})
}

func (h *Handlers) GetCategories(c *fiber.Ctx) error {
	rows, _ := h.db.Pool.Query(context.Background(), "SELECT id,name,slug,COALESCE(icon,''),product_count FROM categories WHERE is_active=true ORDER BY sort_order")
	defer rows.Close()
	var cats []fiber.Map
	for rows.Next() {
		var id,name,slug,icon string; var cnt int
		rows.Scan(&id,&name,&slug,&icon,&cnt)
		cats = append(cats, fiber.Map{"id":id,"name":name,"slug":slug,"icon":icon,"product_count":cnt})
	}
	if cats == nil { cats = []fiber.Map{} }
	return c.JSON(fiber.Map{"success":true,"data":cats})
}

func (h *Handlers) GetStats(c *fiber.Ctx) error {
	var p,v,cat,o int64
	h.db.Pool.QueryRow(context.Background(),"SELECT COUNT(*) FROM products").Scan(&p)
	h.db.Pool.QueryRow(context.Background(),"SELECT COUNT(*) FROM vendors").Scan(&v)
	h.db.Pool.QueryRow(context.Background(),"SELECT COUNT(*) FROM categories").Scan(&cat)
	h.db.Pool.QueryRow(context.Background(),"SELECT COUNT(*) FROM offers").Scan(&o)
	return c.JSON(fiber.Map{"success":true,"data":fiber.Map{"products":p,"vendors":v,"categories":cat,"offers":o}})
}

func (h *Handlers) AdminDashboard(c *fiber.Ctx) error { return h.GetStats(c) }
func (h *Handlers) AdminProducts(c *fiber.Ctx) error { return h.GetProducts(c) }
func (h *Handlers) AdminCategories(c *fiber.Ctx) error { return h.GetCategories(c) }

func (h *Handlers) AdminCreateProduct(c *fiber.Ctx) error {
	var input struct{ Title string `json:"title"`; CategoryID string `json:"category_id"` }
	c.BodyParser(&input)
	id := uuid.New()
	slug := makeSlug(input.Title)
	h.db.Pool.Exec(context.Background(),"INSERT INTO products(id,category_id,title,slug,is_active,created_at,updated_at) VALUES($1,$2::uuid,$3,$4,true,NOW(),NOW())",id,input.CategoryID,input.Title,slug)
	return c.JSON(fiber.Map{"success":true,"data":fiber.Map{"id":id.String(),"slug":slug}})
}

func (h *Handlers) AdminCreateCategory(c *fiber.Ctx) error {
	var input struct{ Name string `json:"name"`; Icon string `json:"icon"` }
	c.BodyParser(&input)
	id := uuid.New()
	slug := makeSlug(input.Name)
	h.db.Pool.Exec(context.Background(),"INSERT INTO categories(id,name,slug,icon,is_active,created_at,updated_at) VALUES($1,$2,$3,$4,true,NOW(),NOW())",id,input.Name,slug,input.Icon)
	return c.JSON(fiber.Map{"success":true,"data":fiber.Map{"id":id.String(),"slug":slug}})
}
