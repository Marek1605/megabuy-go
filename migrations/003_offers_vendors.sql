-- Migration: Add offers and vendors tables
-- Run this on your postgres database

-- Vendors table
CREATE TABLE IF NOT EXISTS vendors (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) NOT NULL,
    slug VARCHAR(255) UNIQUE,
    logo_url TEXT,
    website_url TEXT,
    rating DECIMAL(3,2) DEFAULT 4.5,
    review_count INTEGER DEFAULT 0,
    is_megabuy BOOLEAN DEFAULT false,
    shipping_price DECIMAL(10,2) DEFAULT 0,
    delivery_days VARCHAR(20) DEFAULT '2-3',
    is_active BOOLEAN DEFAULT true,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- Product offers table (for price comparison)
CREATE TABLE IF NOT EXISTS product_offers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id UUID NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    vendor_id UUID REFERENCES vendors(id) ON DELETE SET NULL,
    price DECIMAL(10,2) NOT NULL,
    shipping_price DECIMAL(10,2) DEFAULT 0,
    delivery_days VARCHAR(20) DEFAULT '2-3',
    stock_status VARCHAR(20) DEFAULT 'instock',
    stock_quantity INTEGER DEFAULT 0,
    is_megabuy BOOLEAN DEFAULT false,
    affiliate_url TEXT,
    is_active BOOLEAN DEFAULT true,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- Indexes for fast queries
CREATE INDEX IF NOT EXISTS idx_product_offers_product_id ON product_offers(product_id);
CREATE INDEX IF NOT EXISTS idx_product_offers_vendor_id ON product_offers(vendor_id);
CREATE INDEX IF NOT EXISTS idx_product_offers_price ON product_offers(price);
CREATE INDEX IF NOT EXISTS idx_vendors_slug ON vendors(slug);

-- Add missing columns to products if they don't exist
DO $$ 
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'products' AND column_name = 'gallery_images') THEN
        ALTER TABLE products ADD COLUMN gallery_images JSONB DEFAULT '[]';
    END IF;
    
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'products' AND column_name = 'attributes') THEN
        ALTER TABLE products ADD COLUMN attributes JSONB DEFAULT '{}';
    END IF;
    
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'products' AND column_name = 'rating') THEN
        ALTER TABLE products ADD COLUMN rating DECIMAL(3,2) DEFAULT 0;
    END IF;
    
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'products' AND column_name = 'review_count') THEN
        ALTER TABLE products ADD COLUMN review_count INTEGER DEFAULT 0;
    END IF;
    
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'products' AND column_name = 'mpn') THEN
        ALTER TABLE products ADD COLUMN mpn VARCHAR(100);
    END IF;
    
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'products' AND column_name = 'short_description') THEN
        ALTER TABLE products ADD COLUMN short_description TEXT;
    END IF;
    
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'products' AND column_name = 'stock_quantity') THEN
        ALTER TABLE products ADD COLUMN stock_quantity INTEGER DEFAULT 0;
    END IF;
    
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'products' AND column_name = 'feed_id') THEN
        ALTER TABLE products ADD COLUMN feed_id UUID;
    END IF;
    
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'products' AND column_name = 'affiliate_url') THEN
        ALTER TABLE products ADD COLUMN affiliate_url TEXT;
    END IF;
END $$;

-- Insert default MegaBuy vendor
INSERT INTO vendors (id, name, slug, is_megabuy, rating, review_count, shipping_price, delivery_days)
VALUES ('00000000-0000-0000-0000-000000000001', 'MegaBuy.sk', 'megabuy', true, 4.8, 1250, 0, '1-2')
ON CONFLICT (slug) DO NOTHING;

-- Create indexes on products for faster search
CREATE INDEX IF NOT EXISTS idx_products_slug ON products(slug);
CREATE INDEX IF NOT EXISTS idx_products_ean ON products(ean);
CREATE INDEX IF NOT EXISTS idx_products_sku ON products(sku);
CREATE INDEX IF NOT EXISTS idx_products_brand ON products(brand);
CREATE INDEX IF NOT EXISTS idx_products_category_id ON products(category_id);
CREATE INDEX IF NOT EXISTS idx_products_is_active ON products(is_active);
CREATE INDEX IF NOT EXISTS idx_products_price_min ON products(price_min);
CREATE INDEX IF NOT EXISTS idx_products_created_at ON products(created_at DESC);

-- Full text search index
CREATE INDEX IF NOT EXISTS idx_products_title_trgm ON products USING gin(title gin_trgm_ops);

-- Enable trigram extension if not exists
CREATE EXTENSION IF NOT EXISTS pg_trgm;
