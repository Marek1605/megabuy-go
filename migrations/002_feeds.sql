-- Feeds table
CREATE TABLE IF NOT EXISTS feeds (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) NOT NULL,
    url TEXT NOT NULL,
    type VARCHAR(20) DEFAULT 'xml',
    vendor_id UUID REFERENCES vendors(id) ON DELETE SET NULL,
    schedule VARCHAR(50) DEFAULT 'daily',
    is_active BOOLEAN DEFAULT true,
    xml_item_path VARCHAR(100) DEFAULT 'SHOPITEM',
    field_mapping JSONB DEFAULT '{}',
    last_run TIMESTAMP,
    last_status VARCHAR(50) DEFAULT 'idle',
    product_count INTEGER DEFAULT 0,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- Add feed_id to products
ALTER TABLE products ADD COLUMN IF NOT EXISTS feed_id UUID REFERENCES feeds(id) ON DELETE SET NULL;
ALTER TABLE products ADD COLUMN IF NOT EXISTS affiliate_url TEXT;

-- Index for feed products
CREATE INDEX IF NOT EXISTS idx_products_feed_id ON products(feed_id);

-- Feed import history
CREATE TABLE IF NOT EXISTS feed_history (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    feed_id UUID NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
    status VARCHAR(50) NOT NULL,
    total_items INTEGER DEFAULT 0,
    created INTEGER DEFAULT 0,
    updated INTEGER DEFAULT 0,
    skipped INTEGER DEFAULT 0,
    errors INTEGER DEFAULT 0,
    duration INTEGER DEFAULT 0,
    error_message TEXT,
    started_at TIMESTAMP DEFAULT NOW(),
    finished_at TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_feed_history_feed_id ON feed_history(feed_id);
